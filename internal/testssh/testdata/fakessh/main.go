// Command fakessh stands in for the system ssh in tests. It is a Go program
// rather than a shell script so the same fake runs on Linux and Windows,
// where the Windows client is exercised end to end.
//
// It appends each argv entry on its own line to $FAKE_SSH_LOG, writes
// $FAKE_SSH_STDOUT and $FAKE_SSH_STDERR to the matching stream, echoes stdin
// when $FAKE_SSH_ECHO_STDIN=1, and exits with $FAKE_SSH_EXIT (default 0).
package main

import (
	"io"
	"os"
	"strconv"
)

func main() {
	if log := os.Getenv("FAKE_SSH_LOG"); log != "" {
		var out []byte
		for _, arg := range os.Args[1:] {
			out = append(out, arg...)
			out = append(out, '\n')
		}
		os.WriteFile(log, out, 0o644)
	}
	if s := os.Getenv("FAKE_SSH_STDOUT"); s != "" {
		io.WriteString(os.Stdout, s)
	}
	if s := os.Getenv("FAKE_SSH_STDERR"); s != "" {
		io.WriteString(os.Stderr, s)
	}
	if os.Getenv("FAKE_SSH_ECHO_STDIN") == "1" {
		io.Copy(os.Stdout, os.Stdin)
	}
	code := 0
	if s := os.Getenv("FAKE_SSH_EXIT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			code = n
		}
	}
	os.Exit(code)
}
