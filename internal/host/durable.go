package host

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

// enterGrace is how long after someone asks to enter a box it's left alone.
// The shell starts a moment after the host answers, and must find the box up.
const enterGrace = 2 * time.Minute

// idleScript prints how many processes a box has besides its init, its
// keep-alive sleep and this check. It uses only shell builtins, so it doesn't
// count itself twice. With --init, the keep-alive sleep is init's child.
const idleScript = `n=0
for p in /proc/[0-9]*; do
  p=${p#/proc/}
  [ "$p" = 1 ] && continue
  [ "$p" = "$$" ] && continue
  c=
  read -r c < "/proc/$p/comm" 2>/dev/null || continue
  if [ "$c" = sleep ]; then
    read -r _ _ _ pp _ < "/proc/$p/stat" 2>/dev/null
    [ "$pp" = 1 ] && continue
  fi
  n=$((n + 1))
done
echo $n`

// reconcileBoxes brings durable boxes in line with their records: a container
// with no record goes, a box nobody is using stops, and a box whose privilege
// was revoked loses its container. Volumes always stay; only a reset deletes
// data. A box with an active run, or one entered a moment ago, is left alone.
func (h *Host) reconcileBoxes(ctx context.Context) error {
	boxes, err := h.Store.Boxes(ctx, "")
	if err != nil {
		return err
	}
	names, err := h.Engine.ListDurable(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		if !slices.ContainsFunc(boxes, func(b store.Box) bool { return b.Name == name }) {
			log.Printf("removing durable box %s, which has no record; its volumes stay", name)
			errs = append(errs, h.Engine.Remove(ctx, name))
		}
	}
	live, err := h.Store.Live(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, b := range boxes {
		if err := h.reconcileBox(ctx, b, live); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *Host) reconcileBox(ctx context.Context, b store.Box, live []store.Run) error {
	if slices.ContainsFunc(live, func(r store.Run) bool { return r.Box == b.Name && r.Active() }) || h.recentlyEntered(b.Name) {
		return nil
	}
	running, err := h.Engine.Running(ctx, b.Name)
	if errors.Is(err, engine.ErrNotFound) {
		return nil // The next run creates it again.
	}
	if err != nil {
		return err
	}
	revoked := false
	if b.Privileged {
		granted, err := h.Store.Granted(ctx, b.Repo, task.Privileged)
		if err != nil {
			return err
		}
		revoked = !granted
	}
	if running && h.boxInUse(ctx, b.Name) {
		if revoked {
			log.Printf("box %s is privileged after its grant was revoked; it goes when nothing in it is running", b.Name)
		}
		return nil
	}
	switch {
	case revoked:
		log.Printf("removing box %s's container, because its privilege was revoked; its volumes stay", b.Name)
		return h.Engine.Remove(ctx, b.Name)
	case running:
		return h.Engine.Stop(ctx, b.Name)
	}
	return nil
}

// boxInUse reports whether anything besides the box's own keep-alive is
// running in it, such as a shell someone opened. A box that can't answer
// counts as in use, since stopping it could end someone's work.
func (h *Host) boxInUse(ctx context.Context, name string) bool {
	out, err := h.Engine.Exec(ctx, name, "sh", "-c", idleScript)
	return err != nil || strings.TrimSpace(string(out)) != "0"
}

// noteEntered records that someone is about to open a shell in a box.
func (h *Host) noteEntered(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entered == nil {
		h.entered = map[string]time.Time{}
	}
	h.entered[name] = h.now()
}

func (h *Host) recentlyEntered(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now().Sub(h.entered[name]) < enterGrace
}

// checkBoxesSoon makes the next pass reconcile boxes, as after a revoke.
func (h *Host) checkBoxesSoon() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.boxesDue = true
}

func (h *Host) takeBoxesDue() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	due := h.boxesDue
	h.boxesDue = false
	return due
}
