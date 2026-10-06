package systemd_test

import (
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/systemd"
)

func TestUnitRendersExecAndInstallTarget(t *testing.T) {
	unit := systemd.Unit("/home/dev/bin/pluto")
	for _, want := range []string{
		"ExecStart=/home/dev/bin/pluto daemon",
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestBoxUnitRendersInstanceTemplate(t *testing.T) {
	unit := systemd.BoxUnit("/home/dev/bin/pluto", "/home/dev/.local/state/pluto")
	for _, want := range []string{
		"ExecStart=/home/dev/bin/pluto --state-dir /home/dev/.local/state/pluto box run %i",
		"KillMode=control-group",
		"TimeoutStopSec=30",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("box unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "[Install]") {
		t.Fatalf("box units are started on demand, not enabled:\n%s", unit)
	}
}

func TestBoxUnitQuotesPathsWithSpaces(t *testing.T) {
	unit := systemd.BoxUnit("/opt/my tools/pluto", "/home/dev/my state")
	for _, want := range []string{
		`ExecStart="/opt/my tools/pluto" --state-dir "/home/dev/my state" box run %i`,
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("box unit missing %q:\n%s", want, unit)
		}
	}
}

// TestBoxResourcesDropInRendersCaps pins the cgroup mechanism: memory in MiB
// and CPU bandwidth as a percentage of one CPU.
func TestBoxResourcesDropInRendersCaps(t *testing.T) {
	conf := systemd.BoxResourcesDropIn(4, 8192)
	for _, want := range []string{"[Service]", "MemoryMax=8192M", "CPUQuota=400%"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("drop-in missing %q:\n%s", want, conf)
		}
	}
}
