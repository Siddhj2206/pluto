package runner

import (
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// AutoPauseWindow is the box's effective idle window, read from the contract
// in its worktree. A missing contract keeps the default; an unreadable one
// also keeps the default rather than silently disabling the pause or forcing
// it — a typo in `.pluto.toml` is the handoff's to report.
func (r *Runner) AutoPauseWindow(box *state.Box) time.Duration {
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		return contract.DefaultAutoPause
	}
	return ct.AutoPauseWindow()
}
