package devices_test

import (
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// The trust boundary has one definition: every non-interactive ssh route asks
// for no prompt, a bounded connect, and first-use host-key acceptance. A
// changed key still fails, because the option is accept-new rather than no.
func TestHostOptionsFenceEveryRoute(t *testing.T) {
	got := devices.HostOptions(7 * time.Second)
	want := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=7",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if len(got) != len(want) {
		t.Fatalf("HostOptions = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HostOptions = %q, want %q", got, want)
		}
	}
}
