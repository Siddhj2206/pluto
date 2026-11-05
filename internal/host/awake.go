package host

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

const (
	// awakeFor is how long each sleep block lasts. Renewing short blocks means a
	// crashed host can't leave the machine unable to sleep.
	awakeFor = 2 * time.Minute
	// awakeLead is how far ahead of a scheduled slot the machine stays awake.
	awakeLead = 5 * time.Minute
)

// keepAwake asks the OS not to sleep while there's work now or very soon.
func (h *Host) keepAwake(ctx context.Context) {
	if h.StayAwake == nil || h.now().Sub(h.lastAwake) < awakeFor/2 || !h.wantAwake(ctx) {
		return
	}
	h.lastAwake = h.now()
	h.StayAwake(ctx, awakeFor)
}

func (h *Host) wantAwake(ctx context.Context) bool {
	live, err := h.Store.Live(ctx)
	if err != nil {
		return false
	}
	for _, r := range live {
		if r.Active() {
			return true
		}
	}
	queued, err := h.Store.Queued(ctx)
	if err == nil && len(queued) > 0 {
		return true
	}
	schedules, err := h.Store.Schedules(ctx)
	if err != nil {
		return false
	}
	return slotSoon(schedules, h.now())
}

func slotSoon(schedules []store.Schedule, now time.Time) bool {
	for _, sc := range schedules {
		parsed, err := task.ParseSchedule(sc.Expr)
		if err == nil && parsed.Next(now).Sub(now) <= awakeLead {
			return true
		}
	}
	return false
}

var inhibitWarn sync.Once

// SystemdInhibit blocks sleep, and closing the lid from causing it, for a while.
// It only does so on mains power: a laptop shut in a bag on battery should sleep,
// which Pluto then reports honestly.
func SystemdInhibit(ctx context.Context, d time.Duration) {
	if !onMains("/sys/class/power_supply") {
		return
	}
	cmd := exec.CommandContext(ctx, "systemd-inhibit", "--what=sleep:idle:handle-lid-switch", "--who=Pluto",
		"--why=A run is in progress or due soon", "--mode=block", "sleep", strings.TrimSuffix(d.String(), "0s"))
	if err := cmd.Start(); err != nil {
		inhibitWarn.Do(func() { log.Printf("can't keep the machine awake: %v", err) })
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			inhibitWarn.Do(func() { log.Printf("can't keep the machine awake: %v", err) })
		}
	}()
}

// onMains reports whether the machine has mains power. Some laptops charge over
// USB-C with no mains supply listed, so a battery that's discharging also counts
// as an answer. With nothing to read, such as on a desktop, it assumes mains.
func onMains(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	read := func(name, file string) string {
		data, err := os.ReadFile(filepath.Join(dir, name, file))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	mains, discharging := false, false
	for _, e := range entries {
		switch read(e.Name(), "type") {
		case "Mains":
			if read(e.Name(), "online") == "1" {
				return true
			}
			mains = true
		case "Battery":
			discharging = discharging || read(e.Name(), "status") == "Discharging"
		}
	}
	return !mains && !discharging
}
