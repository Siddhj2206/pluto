package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Siddhj2206/pluto/internal/contract"
)

type githubSetupEvent struct {
	name        string
	section     string
	defaultActs []string
}

var githubSetupEvents = []githubSetupEvent{
	{name: "push", section: "push"},
	{name: "pull_request", section: "pull_request", defaultActs: []string{"opened", "synchronize", "reopened"}},
	{name: "issues", section: "issue", defaultActs: []string{"opened", "edited", "reopened"}},
}

func runSetup(args []string, input io.Reader, stdout, stderr io.Writer) int {
	if maybeHelpAtStart(args, "setup", stdout) {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pluto setup github")
		return 2
	}
	if args[0] != "github" {
		return unknownSubcommand(stderr, "setup", args[0], []string{"github"})
	}
	return runSetupGitHub(args[1:], input, stdout, stderr)
}

func runSetupGitHub(args []string, input io.Reader, stdout, stderr io.Writer) int {
	if maybeHelp(args, "setup github", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("setup github", flag.ContinueOnError)
	if code := parseCommand(fs, args, stderr, "usage: pluto setup github"); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "setup github takes no arguments", "usage: pluto setup github")
	}
	dir, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	if root, _, gitErr := gitInfo(dir); gitErr == nil {
		dir = root
	}
	path := filepath.Join(dir, contract.FileName)
	ct, err := contract.Load(dir)
	if err != nil {
		return fail(stderr, err, contractRunHint(err, "pluto setup github")...)
	}
	jobs := ct.JobNames()
	if len(jobs) == 0 {
		return fail(stderr, errors.New("no declared jobs found; add [jobs.<name>] to .pluto.toml first"))
	}

	reader := bufio.NewReader(input)
	fmt.Fprintln(stdout, "Map GitHub events to declared Pluto jobs in .pluto.toml.")
	fmt.Fprintln(stdout, "Supported events: push, pull_request, issues.")
	fmt.Fprintln(stdout, "Declared jobs:")
	for _, name := range jobs {
		if description := ct.Jobs[name].Description; description != "" {
			fmt.Fprintf(stdout, "  %s — %s\n", name, description)
		} else {
			fmt.Fprintf(stdout, "  %s\n", name)
		}
	}
	fmt.Fprint(stdout, "Events to configure (comma-separated; blank cancels): ")
	selection, err := readSetupLine(reader)
	if err != nil {
		return fail(stderr, err)
	}
	selected, err := parseGitHubSetupSelection(selection)
	if err != nil {
		return fail(stderr, err)
	}
	if len(selected) == 0 {
		fmt.Fprintln(stdout, "GitHub setup cancelled; .pluto.toml was not changed.")
		return 0
	}

	var additions strings.Builder
	for _, event := range githubSetupEvents {
		if !selected[event.name] {
			continue
		}
		if githubEventConfigured(ct, event.section) {
			return fail(stderr, fmt.Errorf("GitHub event %q is already configured; edit its [events.%s] section directly", event.name, event.section))
		}
		job, err := promptSetupJob(reader, stdout, event.name, jobs)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(&additions, "\n[events.%s]\njob = %s\n", event.section, strconv.Quote(job))
		if len(event.defaultActs) > 0 {
			fmt.Fprintf(stdout, "Actions for %s (comma-separated; blank uses %s): ", event.name, strings.Join(event.defaultActs, ", "))
			value, err := readSetupLine(reader)
			if err != nil {
				return fail(stderr, err)
			}
			actions, err := parseSetupActions(value, event.defaultActs)
			if err != nil {
				return fail(stderr, err)
			}
			quoted := make([]string, len(actions))
			for i, action := range actions {
				quoted[i] = strconv.Quote(action)
			}
			fmt.Fprintf(&additions, "actions = [%s]\n", strings.Join(quoted, ", "))
		}
	}

	current, err := os.ReadFile(path)
	if err != nil {
		return fail(stderr, err)
	}
	updated := string(current) + additions.String()
	if _, err := contract.Parse(updated); err != nil {
		return fail(stderr, fmt.Errorf("generated GitHub mapping is not a valid contract: %w", err))
	}
	info, err := os.Stat(path)
	if err != nil {
		return fail(stderr, err)
	}
	if err := writeContractAtomic(path, []byte(updated), info.Mode().Perm()); err != nil {
		return fail(stderr, fmt.Errorf("write %s: %w", contract.FileName, err))
	}
	fmt.Fprintf(stdout, "configured GitHub events in %s\n", path)
	fmt.Fprintln(stdout, "next: approve this exact contract revision through the local daemon contract-trust API before unattended event work")
	return 0
}

func writeContractAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pluto.toml-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func parseGitHubSetupSelection(value string) (map[string]bool, error) {
	selected := make(map[string]bool)
	if strings.TrimSpace(value) == "" {
		return selected, nil
	}
	valid := make(map[string]bool, len(githubSetupEvents))
	for _, event := range githubSetupEvents {
		valid[event.name] = true
	}
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(raw)
		if !valid[name] {
			return nil, fmt.Errorf("unsupported GitHub event %q (choose push, pull_request, or issues)", name)
		}
		if selected[name] {
			return nil, fmt.Errorf("GitHub event %q was selected more than once", name)
		}
		selected[name] = true
	}
	return selected, nil
}

func promptSetupJob(reader *bufio.Reader, stdout io.Writer, event string, jobs []string) (string, error) {
	fmt.Fprintf(stdout, "Job for %s [%s]: ", event, strings.Join(jobs, ", "))
	value, err := readSetupLine(reader)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	for _, job := range jobs {
		if value == job {
			return job, nil
		}
	}
	return "", fmt.Errorf("%q is not a declared job", value)
}

func parseSetupActions(value string, defaults []string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return append([]string(nil), defaults...), nil
	}
	seen := make(map[string]bool)
	actions := strings.Split(value, ",")
	for i := range actions {
		actions[i] = strings.TrimSpace(actions[i])
		if actions[i] == "" || seen[actions[i]] {
			return nil, errors.New("actions must be non-empty and unique")
		}
		seen[actions[i]] = true
	}
	return actions, nil
}

func githubEventConfigured(ct *contract.Contract, section string) bool {
	switch section {
	case "push":
		return ct.Events.Push != nil
	case "pull_request":
		return ct.Events.PullRequest != nil
	case "issue":
		return ct.Events.Issue != nil
	default:
		return false
	}
}

func readSetupLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
