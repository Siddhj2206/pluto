package cli

import (
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
)

func TestAttachArgsProxyVsock(t *testing.T) {
	info := api.AttachInfo{User: "dev", UDS: "/state/boxes/abc/v.sock", Key: "/state/boxes/abc/id", Port: 22}
	args := attachArgs(info, "/usr/local/bin/pluto", nil)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-i /state/boxes/abc/id",
		"ProxyCommand=/usr/local/bin/pluto vsock connect /state/boxes/abc/v.sock 22",
		"dev@box",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}

func TestAttachArgsQuotesPathsWithSpaces(t *testing.T) {
	info := api.AttachInfo{User: "dev", UDS: "/home/dev/my state/v.sock", Key: "/home/dev/my state/id", Port: 22}
	args := attachArgs(info, "/opt/my tools/pluto", nil)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "ProxyCommand='/opt/my tools/pluto' vsock connect '/home/dev/my state/v.sock' 22") {
		t.Fatalf("proxy command not quoted: %v", args)
	}
}

func TestAttachArgsPassesCommand(t *testing.T) {
	args := attachArgs(api.AttachInfo{User: "dev", Port: 22}, "/bin/pluto", []string{"uname", "-a"})
	if len(args) < 2 || args[len(args)-2] != "uname" || args[len(args)-1] != "-a" {
		t.Fatalf("command not passed through: %v", args)
	}
}
