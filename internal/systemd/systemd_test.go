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
// and CPU bandwidth as a percentage of one CPU. MemoryMax is the guest's RAM
// plus headroom for the VMM and page tables, not the RAM verbatim, so a
// 1024M guest is not OOM-killed by the VMM's own footprint (see docs/contract.md).
func TestBoxResourcesDropInRendersCaps(t *testing.T) {
	conf := systemd.BoxResourcesDropIn(systemd.BoxResources{CPUs: 4, MemoryMiB: 8192})
	for _, want := range []string{"[Service]", "MemoryMax=9216M", "CPUQuota=400%"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("drop-in missing %q:\n%s", want, conf)
		}
	}
}

// TestBoxMemoryMaxAddsOverhead pins the overhead margin independently of the
// drop-in rendering: the larger of a fixed 256 MiB and one-eighth of guest RAM,
// added to the guest's declared size.
func TestBoxMemoryMaxAddsOverhead(t *testing.T) {
	cases := map[int]int{
		1024: 1280, // floor: 256 > 1024/8
		2048: 2304, // floor: 256 > 2048/8
		4096: 4608, // one-eighth: 512 > 256
		8192: 9216, // one-eighth: 1024 > 256
	}
	for guestMiB, want := range cases {
		if got := systemd.BoxMemoryMaxMiB(guestMiB); got != want {
			t.Errorf("BoxMemoryMaxMiB(%d) = %d, want %d", guestMiB, got, want)
		}
	}
}
