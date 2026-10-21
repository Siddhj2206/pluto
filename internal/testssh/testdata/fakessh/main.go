// Command fakessh stands in for the system ssh in tests. It is a Go program
// rather than a shell script so the same fake runs on Linux and Windows,
// where the Windows client is exercised end to end.
//
// It appends each argv entry on its own line to $FAKE_SSH_LOG, writes
// $FAKE_SSH_STDOUT and $FAKE_SSH_STDERR to the matching stream, echoes stdin
// when $FAKE_SSH_ECHO_STDIN=1, saves stdin to $FAKE_SSH_STDIN_LOG when set,
// and exits with $FAKE_SSH_EXIT (default 0).
//
// $FAKE_SSH_MARKER models key bootstrap: a call whose remote command mentions
// authorized_keys writes the marker and succeeds; after that every call
// succeeds. Before it, calls fail with the configured code and stderr. This
// lets a test observe the probe-fails, install-key, probe-succeeds sequence.
package main

import (
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	args := os.Args[1:]
	if log := os.Getenv("FAKE_SSH_LOG"); log != "" {
		os.WriteFile(log, []byte(strings.Join(args, "\n")+"\n"), 0o644)
	}

	var stdin []byte
	if os.Getenv("FAKE_SSH_ECHO_STDIN") == "1" || os.Getenv("FAKE_SSH_STDIN_LOG") != "" {
		stdin, _ = io.ReadAll(os.Stdin)
	}
	if p := os.Getenv("FAKE_SSH_STDIN_LOG"); p != "" {
		os.WriteFile(p, stdin, 0o644)
	}

	code := 0
	if s := os.Getenv("FAKE_SSH_EXIT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			code = n
		}
	}

	marker := os.Getenv("FAKE_SSH_MARKER")
	if marker != "" {
		install := false
		for _, arg := range args {
			if strings.Contains(arg, "authorized_keys") {
				install = true
				break
			}
		}
		if install {
			os.WriteFile(marker, []byte("installed\n"), 0o644)
			code = 0
		} else if _, err := os.Stat(marker); err == nil {
			code = 0
		}
	}

	if s := os.Getenv("FAKE_SSH_STDOUT"); s != "" {
		io.WriteString(os.Stdout, s)
	}
	if code != 0 {
		if s := os.Getenv("FAKE_SSH_STDERR"); s != "" {
			io.WriteString(os.Stderr, s)
		}
	}
	if os.Getenv("FAKE_SSH_ECHO_STDIN") == "1" {
		os.Stdout.Write(stdin)
	}
	os.Exit(code)
}
