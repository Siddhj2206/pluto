package runner

import (
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
)

func TestPublicForwardUsesOnlySelectedGuestServicePort(t *testing.T) {
	args := sshDirectArgs("/opt/pluto cli", api.AttachInfo{User: "dev", UDS: "/state/box with space/v.sock", Key: "/state/private key", Port: 22}, 3000)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "ProxyCommand='/opt/pluto cli' vsock connect '/state/box with space/v.sock' 22") {
		t.Fatalf("proxy command did not use the box's vsock SSH endpoint: %s", joined)
	}
	if !strings.Contains(joined, "127.0.0.1:3000") {
		t.Fatalf("forward target = %s, want only selected guest port", joined)
	}
	if strings.Contains(joined, "127.0.0.1:22") {
		t.Fatalf("forward command targets guest SSH instead of selected service: %s", joined)
	}
}
