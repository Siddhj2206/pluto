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
