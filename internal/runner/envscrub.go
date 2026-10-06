package runner

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// scrubEnvironment removes box-owned work and runtime identity from an offline
// rootfs copy before it is published as a reusable layer. Installed tools,
// package caches, and project build artifacts survive; another box's worktree,
// branch/PR state, agent logs, jobs, sessions, shell history, and ssh identity
// do not. Declared [env] and [provision].env are refused at parse time for a
// cached provision, so there is no declared secret to scrub here.
func scrubEnvironment(image string) error {
	// /home/dev/work holds the worktree, branches, and any PR/issue checkout.
	// /home/dev/.local/state/pluto holds agent status, jobs, logs, sessions,
	// service and hook scripts (which may embed environment). /home/dev/.ssh
	// holds the per-box authorized_keys. The credentials file can be left by a
	// provision that fetched over HTTPS.
	for _, path := range []string{
		"/home/dev/work",
		"/home/dev/.local/state/pluto",
		"/home/dev/.ssh",
		"/home/dev/.git-credentials",
	} {
		if err := removeExtTree(image, path); err != nil {
			return err
		}
	}
	return nil
}

// removeExtTree recursively removes one path from an ext4 image with debugfs.
// A directory's children are removed deepest-first because debugfs rmdir only
// removes an empty directory; a plain file is unlinked directly. A path that
// does not exist is a no-op: the exact set of paths a provision creates is not
// guaranteed.
func removeExtTree(image, path string) error {
	entries, isDir, exists, err := debugfsList(image, path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !isDir {
		return debugfsRemove(image, "rm "+debugfsQuote(path))
	}
	for _, entry := range entries {
		child := filepath.ToSlash(filepath.Join(path, entry.name))
		if err := removeExtTree(image, child); err != nil {
			return err
		}
	}
	return debugfsRemove(image, "rmdir "+debugfsQuote(path))
}

type extEntry struct {
	name string
}

// debugfsList lists a directory with debugfs's machine-readable `ls -p`
// format: /<inode>/<mode>/<uid>/<gid>/<name>/<size>/ for files and a trailing
// empty field for directories. A missing path reports exists=false; a regular
// file reports isDir=false so the caller unlinks it instead of recursing.
func debugfsList(image, path string) (entries []extEntry, isDir, exists bool, err error) {
	out, listErr := exec.Command("debugfs", "-R", "ls -p "+debugfsQuote(path), image).CombinedOutput()
	text := string(out)
	if listErr != nil {
		return nil, false, false, fmt.Errorf("list environment path %s: %w (%s)", path, listErr, strings.TrimSpace(text))
	}
	if strings.Contains(text, "File not found") {
		return nil, false, false, nil
	}
	if strings.Contains(text, "is not a directory") {
		return nil, false, true, nil
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(line, "/")
		if len(fields) < 6 || fields[0] != "" {
			continue
		}
		name := fields[5]
		if name == "." || name == ".." || name == "" {
			continue
		}
		if strings.ContainsAny(name, "\r\n") {
			return nil, false, false, fmt.Errorf("unsupported filename while scrubbing %s", path)
		}
		entries = append(entries, extEntry{name: name})
	}
	return entries, true, true, nil
}

// debugfsRemove runs one mutating debugfs command, treating a vanished path as
// an error because the caller's recursion expects it to have existed.
func debugfsRemove(image, command string) error {
	out, err := exec.Command("debugfs", "-w", "-R", command, image).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "File not found") {
			return fmt.Errorf("scrub environment (%s): file not found", command)
		}
		return fmt.Errorf("scrub environment (%s): %w (%s)", command, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func debugfsQuote(path string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
}
