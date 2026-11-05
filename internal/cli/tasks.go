package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/Siddhj2206/pluto/internal/host"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

// repoInfo is the repo the current directory belongs to.
type repoInfo struct {
	root string
	key  string // Its name on the host.
	url  string // Where the host can fetch it, if anywhere.
}

var notKeyChars = regexp.MustCompile(`[^A-Za-z0-9_./-]+`)

// remoteKey names a repo from its remote. GitHub repos use owner/name, matching
// pluto ci connect, and get an https URL that works without an SSH agent.
func remoteKey(remote string) (key, fetchURL string) {
	if repo, ok := repoFromRemote(remote); ok {
		return repo, "https://github.com/" + repo + ".git"
	}
	clean := stripCredentials(remote)
	key = clean
	if _, after, ok := strings.Cut(key, "://"); ok {
		key = after
	}
	key = strings.TrimSuffix(strings.TrimSuffix(key, "/"), ".git")
	return notKeyChars.ReplaceAllString(key, "-"), clean
}

func currentRepo() (repoInfo, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return repoInfo{}, &userError{msg: "this directory isn't in a git repo.", next: "cd into a repo", code: exitError}
	}
	info := repoInfo{root: strings.TrimSpace(string(out))}
	remote, err := exec.Command("git", "-C", info.root, "remote", "get-url", "origin").Output()
	if err != nil {
		// The path keeps two repos with the same directory name apart. They'd
		// otherwise share a box, secrets and privilege.
		sum := sha256.Sum256([]byte(info.root))
		info.key = "local/" + filepath.Base(info.root) + "-" + hex.EncodeToString(sum[:3])
		return info, nil
	}
	info.key, info.url = remoteKey(strings.TrimSpace(string(remote)))
	return info, nil
}

func defaultBranch(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		return "", &userError{msg: "git doesn't know this repo's default branch.", next: "git remote set-head origin --auto   (or pass --branch <name>)", code: exitError}
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/"), nil
}

// workingTree streams the repo's tracked files, as they are now, plus new files
// git doesn't ignore. Ignored files stay out, as do git's own files.
func workingTree(root string, w io.Writer) error {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return fmt.Errorf("listing the repo's files: %w", err)
	}
	tw := tar.NewWriter(w)
	names := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		if name == "" {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // Deleted but not yet committed.
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			continue // A submodule.
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = name, 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.CopyN(tw, f, info.Size())
			f.Close()
			if err != nil {
				return fmt.Errorf("reading %s: %w", name, err)
			}
		}
	}
	return tw.Close()
}

func (c *client) upload(root string) (string, error) {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(workingTree(root, pw)) }()
	req, err := http.NewRequest(http.MethodPost, "http://pluto/v1/uploads", pr)
	if err != nil {
		return "", err
	}
	long := *c.http
	long.Timeout = 0 // A big repo takes as long as it takes.
	resp, err := long.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		var f host.Failure
		if json.Unmarshal(data, &f) == nil && f.Error != "" {
			return "", &userError{msg: f.Error, next: f.Next, code: exitError}
		}
		return "", fmt.Errorf("the host returned %s", resp.Status)
	}
	var out host.UploadResponse
	return out.Upload, json.Unmarshal(data, &out)
}

// commandLine turns arguments into shell. One argument is taken as shell itself,
// so pipes and && work when quoted. Several keep their boundaries, as in a normal
// command: grep "foo bar" . searches for "foo bar".
func commandLine(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

func runCommand(args []string) error {
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	cfg, err := task.LoadDir(repo.root)
	if err != nil {
		return &userError{msg: err.Error(), code: exitError}
	}
	var t task.Task
	switch {
	case len(args) >= 2 && args[0] == "--":
		t = task.Task{Name: "command", Run: []string{commandLine(args[1:])}, Timeout: task.DefaultTimeout}
	case len(args) == 1 && !strings.HasPrefix(args[0], "-"):
		var ok bool
		if t, ok = cfg.Tasks[args[0]]; !ok {
			names := make([]string, 0, len(cfg.Tasks))
			for name := range cfg.Tasks {
				names = append(names, name)
			}
			slices.Sort(names)
			msg := "no task named " + args[0] + " in " + task.Dir
			if len(names) > 0 {
				msg += ". Tasks: " + strings.Join(names, ", ")
			}
			return &userError{msg: msg + ".", code: exitError}
		}
	default:
		return usageError("usage: pluto run <task> | pluto run -- <command>")
	}
	c := newClient()
	if err := c.require("tasks"); err != nil {
		return err
	}
	upload, err := c.upload(repo.root)
	if err != nil {
		return err
	}
	var run store.Run
	req := host.RunTaskRequest{Repo: repo.key, Upload: upload, Plan: host.Plan{Task: t, Image: cfg.Image, Requires: cfg.Requires}}
	if err := c.json(http.MethodPost, "/v1/tasks/run", req, &run); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Run %s. Ctrl-C stops watching, and the run keeps going. To stop it: pluto cancel %s\n", run.ID, run.ID)
	state, err := followRun(c, run.ID)
	if err != nil {
		return err
	}
	var final store.Run
	if err := c.json(http.MethodGet, "/v1/runs/"+url.PathEscape(run.ID), nil, &final); err != nil {
		return err
	}
	if state == store.StatePassed {
		fmt.Fprintf(os.Stderr, "Run %s passed.\n", run.ID)
		return nil
	}
	msg := fmt.Sprintf("run %s %s", run.ID, state)
	if n := note(final); n != "" {
		msg += ": " + n
	}
	next := ""
	if !final.StartedAt.IsZero() && state != store.StateCancelled {
		next = "pluto enter " + run.ID // A run that never started has nothing to look at.
	}
	return &userError{msg: msg, next: next, code: exitError}
}

// followPatience is how long watching a run survives the host being unreachable.
const followPatience = time.Minute

// followRun prints a task's log as it grows, and returns the run's final state.
func followRun(c *client, id string) (string, error) {
	var offset int64
	var hostGone time.Time
	for {
		req, err := http.NewRequest(http.MethodGet, "http://pluto/v1/runs/"+url.PathEscape(id)+"/logs?offset="+fmt.Sprint(offset), nil)
		if err != nil {
			return "", err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			// The run outlives the host, so a restart shouldn't end the watching.
			if hostGone.IsZero() {
				hostGone = time.Now()
			}
			if time.Since(hostGone) > followPatience {
				return "", errHostDown
			}
			time.Sleep(time.Second)
			continue
		}
		hostGone = time.Time{}
		if resp.StatusCode >= 300 {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var f host.Failure
			if json.Unmarshal(data, &f) == nil && f.Error != "" {
				return "", &userError{msg: f.Error, next: f.Next, code: exitError}
			}
			return "", fmt.Errorf("the host returned %s", resp.Status)
		}
		state := resp.Header.Get(host.HeaderState)
		if state == "" {
			resp.Body.Close()
			return "", &userError{msg: "run " + id + " is a CI job, so its log appears when GitHub publishes it.", next: "pluto logs " + id, code: exitError}
		}
		n, err := io.Copy(os.Stdout, resp.Body)
		resp.Body.Close()
		offset += n
		if err != nil {
			// The host went away mid-read. Pick up where this left off.
			hostGone = time.Now()
			time.Sleep(time.Second)
			continue
		}
		if state != store.StateQueued && state != store.StateWaiting && state != store.StateRunning {
			return state, nil
		}
		time.Sleep(time.Second)
	}
}

func cancelCommand(args []string) error {
	id, err := oneRunArg("cancel", args)
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("tasks"); err != nil {
		return err
	}
	if _, err := c.raw(http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/cancel", nil); err != nil {
		return err
	}
	fmt.Printf("Cancelling %s.\n", id)
	return nil
}

func scheduleCommand(args []string) error {
	if len(args) == 0 {
		return usageError("usage: pluto schedule enable [--branch <name>] | disable | ls")
	}
	c := newClient()
	if err := c.require("schedules"); err != nil {
		return err
	}
	switch args[0] {
	case "ls":
		var list host.ScheduleList
		if err := c.json(http.MethodGet, "/v1/schedules", nil, &list); err != nil {
			return err
		}
		return printSchedules(list)
	case "enable":
		repo, err := currentRepo()
		if err != nil {
			return err
		}
		if repo.url == "" {
			return &userError{msg: "this repo has no remote named origin, and schedules read from one.", code: exitError}
		}
		var branch string
		switch {
		case len(args) == 3 && args[1] == "--branch":
			branch = args[2]
		case len(args) == 1:
			if branch, err = defaultBranch(repo.root); err != nil {
				return err
			}
		default:
			return usageError("usage: pluto schedule enable [--branch <name>]")
		}
		c.http.Timeout = 0 // A first fetch can take a while on a home connection.
		var list host.ScheduleList
		req := host.ScheduleEnableRequest{Repo: repo.key, URL: repo.url, Branch: branch}
		if err := c.json(http.MethodPost, "/v1/schedules", req, &list); err != nil {
			return err
		}
		fmt.Printf("Schedules for %s now follow the committed %s branch, in this machine's local time.\n", repo.key, branch)
		return printSchedules(list)
	case "disable":
		repo, err := currentRepo()
		if err != nil {
			return err
		}
		if _, err := c.raw(http.MethodDelete, "/v1/schedules?repo="+url.QueryEscape(repo.key), nil); err != nil {
			return err
		}
		fmt.Printf("Schedules for %s are off. Its box and runs stay.\n", repo.key)
		return nil
	}
	return usageError("unknown schedule command " + args[0])
}

func printSchedules(list host.ScheduleList) error {
	for _, s := range list.Sources {
		if s.Problem != "" {
			fmt.Printf("%s (%s): using the last good config. Problem: %s\n", s.Repo, s.Branch, s.Problem)
		}
	}
	if len(list.Schedules) == 0 {
		fmt.Println("No scheduled tasks. Add a schedule: to a task in .pluto, commit it, and it appears within 15 minutes.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tTASK\tSCHEDULE\tNEXT\tLAST\tNOTE")
	for _, s := range list.Schedules {
		last := "-"
		if s.LastRun != "" {
			last = s.LastRun + " " + s.LastState
		}
		next := "-"
		if !s.Next.IsZero() {
			next = s.Next.Format("Jan 2 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Repo, s.Task, s.Expr, next, last, s.LastNote)
	}
	return tw.Flush()
}

func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "unknown"
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}

func boxCommand(args []string) error {
	if len(args) == 0 {
		return usageError("usage: pluto box ls | reset [--yes] | grant privileged | revoke privileged")
	}
	c := newClient()
	if err := c.require("boxes"); err != nil {
		return err
	}
	switch args[0] {
	case "ls":
		c.http.Timeout = 0 // Measuring a big build cache can take a while.
		var boxes []host.BoxInfo
		if err := c.json(http.MethodGet, "/v1/boxes", nil, &boxes); err != nil {
			return err
		}
		if len(boxes) == 0 {
			fmt.Println("No boxes yet. A repo gets one the first time it runs a task.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "REPO\tSTATE\tSIZE\tPRIVILEGED\tIMAGE")
		for _, b := range boxes {
			state := "stopped"
			if b.Running {
				state = "running"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\n", b.Repo, state, humanBytes(b.Bytes), b.Privileged, b.Image)
		}
		return tw.Flush()
	case "reset":
		repo, err := currentRepo()
		if err != nil {
			return err
		}
		if !(len(args) == 2 && args[1] == "--yes") {
			if len(args) != 1 {
				return usageError("usage: pluto box reset [--yes]")
			}
			if !confirm(fmt.Sprintf("Delete %s's box, including its /workspace and home directory?", repo.key)) {
				return &userError{msg: "nothing was deleted.", code: exitError}
			}
		}
		if _, err := c.raw(http.MethodPost, "/v1/boxes/reset", host.RepoRequest{Repo: repo.key}); err != nil {
			return err
		}
		fmt.Printf("Reset %s's box. Its next run starts clean, with a fresh copy of the image.\n", repo.key)
		return nil
	case "grant", "revoke":
		if len(args) != 2 || args[1] != task.Privileged {
			return usageError("usage: pluto box " + args[0] + " " + task.Privileged)
		}
		repo, err := currentRepo()
		if err != nil {
			return err
		}
		if args[0] == "revoke" {
			q := url.Values{"repo": {repo.key}, "name": {task.Privileged}}
			if _, err := c.raw(http.MethodDelete, "/v1/grants?"+q.Encode(), nil); err != nil {
				return err
			}
			fmt.Printf("%s can no longer use privileged boxes.\n", repo.key)
			return nil
		}
		if _, err := c.raw(http.MethodPost, "/v1/grants", host.RepoRequest{Repo: repo.key, Name: task.Privileged}); err != nil {
			return err
		}
		fmt.Printf("%s can now use privileged boxes. Code in this repo can act with your user's rights on this machine.\n", repo.key)
		return nil
	}
	return usageError("unknown box command " + args[0])
}

func confirm(question string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Print(question + " [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

func secretCommand(args []string) error {
	if len(args) == 0 {
		return usageError("usage: pluto secret set <NAME> | ls | rm <NAME>")
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("secrets"); err != nil {
		return err
	}
	switch {
	case args[0] == "ls" && len(args) == 1:
		var names []string
		if err := c.json(http.MethodGet, "/v1/secrets?repo="+url.QueryEscape(repo.key), nil, &names); err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Printf("%s has no secrets.\n", repo.key)
			return nil
		}
		fmt.Println(strings.Join(names, "\n"))
		return nil
	case args[0] == "set" && len(args) == 2:
		value, err := readSecret(args[1])
		if err != nil {
			return err
		}
		if _, err := c.raw(http.MethodPost, "/v1/secrets", host.RepoRequest{Repo: repo.key, Name: args[1], Value: value}); err != nil {
			return err
		}
		fmt.Printf("Set %s for %s on this host. Tasks get it by listing it under secrets:.\n", args[1], repo.key)
		return nil
	case args[0] == "rm" && len(args) == 2:
		q := url.Values{"repo": {repo.key}, "name": {args[1]}}
		if _, err := c.raw(http.MethodDelete, "/v1/secrets?"+q.Encode(), nil); err != nil {
			return err
		}
		fmt.Printf("Removed %s.\n", args[1])
		return nil
	}
	return usageError("usage: pluto secret set <NAME> | ls | rm <NAME>")
}

// readSecret takes a value from a hidden prompt or a pipe, so it can't land in
// shell history the way an argument would.
func readSecret(name string) (string, error) {
	fd := int(os.Stdin.Fd())
	var data []byte
	var err error
	if term.IsTerminal(fd) {
		fmt.Printf("Value for %s (hidden): ", name)
		data, err = term.ReadPassword(fd)
		fmt.Println()
	} else {
		data, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return "", err
	}
	data = bytes.TrimRight(data, "\r\n")
	if len(data) == 0 {
		return "", &userError{msg: "no value was provided.", code: exitUsage}
	}
	return string(data), nil
}
