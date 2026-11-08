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
	"time"

	"golang.org/x/term"

	"github.com/Siddhj2206/pluto/internal/host"
	"github.com/Siddhj2206/pluto/internal/store"
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
	// Other hosts and paths can be case-sensitive, so their keys keep their case.
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

// followPatience is how long watching a run survives the host being unreachable.
const followPatience = time.Minute

// followRun prints a task's log as it grows, and returns the run's final state.
func followRun(c *client, id string) (string, error) {
	var offset int64
	var hostGone time.Time
	var waiting string
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
		if w := resp.Header.Get(host.HeaderWaiting); w != "" && w != waiting {
			fmt.Fprintln(os.Stderr, "Queued, "+w+".")
			if next := resp.Header.Get(host.HeaderWaitingNext); next != "" {
				fmt.Fprintln(os.Stderr, "  Next: "+next)
			}
		}
		waiting = resp.Header.Get(host.HeaderWaiting)
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

func confirm(question string) bool {
	if yes {
		return true
	}
	if noInput || !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Fprint(os.Stderr, question+" [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

// readSecret takes a value from a hidden prompt or a pipe, so it can't land in
// shell history the way an argument would.
func readSecret(name string) (string, error) {
	fd := int(os.Stdin.Fd())
	var data []byte
	var err error
	if term.IsTerminal(fd) {
		if noInput {
			return "", &userError{msg: "--no-input is set, so the value has to come from a pipe.", next: "printf %s \"$VALUE\" | pluto secret set " + name, code: exitUsage}
		}
		fmt.Fprintf(os.Stderr, "Value for %s (hidden): ", name)
		data, err = term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
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
