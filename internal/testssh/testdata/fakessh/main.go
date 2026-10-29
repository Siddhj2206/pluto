// Command fakessh stands in for the system ssh in tests. It is a Go program
// rather than a shell script so the same fake runs on Linux and Windows,
// where the Windows client is exercised end to end.
//
// It appends each argv entry on its own line to $FAKE_SSH_LOG, writes
// $FAKE_SSH_STDOUT and $FAKE_SSH_STDERR to the matching stream, echoes stdin
// when $FAKE_SSH_ECHO_STDIN=1, saves stdin to $FAKE_SSH_STDIN_LOG when set,
// and exits with $FAKE_SSH_EXIT (default 0).
//
// $FAKE_SSH_SCRIPT is an optional JSON array of rules. The first rule whose
// match string appears in the joined argv supplies stdout, stderr, and exit
// status. This lets a test model `ssh -G`, a probe, a version handshake, and
// `ls` in one session without the fake knowing Pluto's commands.
//
// $FAKE_SSH_CALL_LOG appends every invocation, separated by a line holding
// only `---`, so a test can assert the order of a multi-call flow.
//
// $FAKE_SSH_MARKER models key bootstrap: a call whose remote command mentions
// authorized_keys writes the marker and succeeds; after that every call
// succeeds. Before it, calls fail with the configured code and stderr. This
// lets a test observe the probe-fails, install-key, probe-succeeds sequence.
package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type rule struct {
	Match  string `json:"match"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

func main() {
	// The same binary stands in for ssh-copy-id. When it is invoked under that
	// name it writes the key-bootstrap marker and exits successfully.
	if marker := os.Getenv("FAKE_SSH_COPYID_MARKER"); marker != "" && strings.HasPrefix(filepath.Base(os.Args[0]), "ssh-copy-id") {
		os.WriteFile(marker, []byte("installed\n"), 0o644)
		os.Exit(0)
	}
	args := os.Args[1:]
	joined := strings.Join(args, " ")
	if log := os.Getenv("FAKE_SSH_LOG"); log != "" {
		os.WriteFile(log, []byte(strings.Join(args, "\n")+"\n"), 0o644)
	}
	if log := os.Getenv("FAKE_SSH_CALL_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			f.WriteString(strings.Join(args, "\n") + "\n---\n")
			f.Close()
		}
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
	stdout := os.Getenv("FAKE_SSH_STDOUT")
	stderr := os.Getenv("FAKE_SSH_STDERR")
	if script := os.Getenv("FAKE_SSH_SCRIPT"); script != "" {
		var rules []rule
		if json.Unmarshal([]byte(script), &rules) == nil {
			for _, r := range rules {
				if r.Match == "" || strings.Contains(joined, r.Match) {
					stdout, stderr, code = r.Stdout, r.Stderr, r.Exit
					break
				}
			}
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

	if stdout != "" {
		io.WriteString(os.Stdout, stdout)
	}
	if code != 0 && stderr != "" {
		io.WriteString(os.Stderr, stderr)
	}
	if os.Getenv("FAKE_SSH_ECHO_STDIN") == "1" {
		os.Stdout.Write(stdin)
	}
	os.Exit(code)
}
