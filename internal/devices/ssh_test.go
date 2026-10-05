package devices

import (
	"strings"
	"testing"
)

func TestSSHArgsBoundTheWaitAndFenceTheTarget(t *testing.T) {
	args := sshArgs("siddhant@neptuno", []string{"pluto", "version"})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"BatchMode=yes",
		"ConnectTimeout=5",
		"-- siddhant@neptuno",
		"pluto version",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ssh args missing %q: %v", want, args)
		}
	}
}

func TestSSHArgsQuoteRemoteWords(t *testing.T) {
	args := sshArgs("host", []string{"echo", "hello world", "it's"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "'hello world'") {
		t.Fatalf("spaced word not quoted: %v", args)
	}
	if !strings.Contains(joined, `'it'\''s'`) {
		t.Fatalf("quote in word not escaped: %v", args)
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine("banner\n\nssh: connect to host x: refused\n"); got != "ssh: connect to host x: refused" {
		t.Fatalf("lastLine = %q", got)
	}
	if got := lastLine(""); got != "" {
		t.Fatalf("lastLine of empty = %q", got)
	}
}
