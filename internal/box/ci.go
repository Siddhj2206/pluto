package box

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// CreateEphemeral creates a fresh ephemeral box for one CI run, seeded from the
// workspace's provisioned package layer. It mounts no durable volumes — the
// box's root is the base and package layers plus a throwaway writable layer —
// so CI can never read or mutate the workspace's durable box volumes (#148,
// ADR 0017). The box is started before it is returned; a failure to start
// removes it rather than leaving a half-built box behind.
func (m *Manager) CreateEphemeral(ctx context.Context, workspace, image string) (*state.Box, error) {
	if m.adapter == nil || m.engine == nil {
		return nil, errors.New("box lifecycle is not configured")
	}
	ws, rev, err := m.adapter.Resolve(ctx, workspace)
	if err != nil {
		return nil, err
	}
	repo := ws.Remote
	if repo == "" {
		repo = ws.Path
	}
	project, err := contract.LoadProject(ws.Path)
	if err != nil {
		return nil, err
	}
	layers, err := m.boxLayers(ctx, project, ws.Path, image)
	if err != nil {
		return nil, err
	}
	box, err := m.store.CreateEphemeralBox(ws.Path, repo, rev.Commit)
	if err != nil {
		return nil, err
	}
	engineID, err := m.engine.Create(ctx, container.CreateSpec{
		BoxID:  box.ID,
		Image:  seedImage(layers),
		Layers: layers,
		Engine: engineCapability(project.Docker),
	})
	if err != nil {
		_ = m.Remove(ctx, box.ID)
		return nil, err
	}
	box, err = m.store.SetBoxRuntime(box.ID, engineID, layerRecords(layers), nil)
	if err != nil {
		_ = m.Remove(ctx, box.ID)
		return nil, err
	}
	started, err := m.start(ctx, box)
	if err != nil {
		_ = m.Remove(ctx, box.ID)
		return nil, err
	}
	return started, nil
}

// Discard removes an ephemeral box: its container and its record. A durable
// box is refused, so CI cleanup can never destroy a workspace's box.
func (m *Manager) Discard(ctx context.Context, id string) error {
	box, err := m.store.Box(id)
	if err != nil {
		return err
	}
	if !box.Ephemeral {
		return fmt.Errorf("box %s is not ephemeral; refusing to discard", state.ShortID(id))
	}
	return m.Remove(ctx, id)
}

// Retain marks an ephemeral box retained at at, so its failed run's box is kept
// for inspection and attach. Retention eviction is bounded by age and count
// unless the box is pinned.
func (m *Manager) Retain(id string, at time.Time) (*state.Box, error) {
	return m.store.SetRetained(id, at)
}

// Pin exempts a box from retention eviction. It stays on the manager, not the
// store, so every box-state mutation the daemon performs rides the one box
// lifecycle seam it holds by interface — and tests drive retention through the
// same seam.
func (m *Manager) Pin(id string) (*state.Box, error) { return m.store.SetPinned(id, true) }

// Unpin lifts a box's retention exemption. See Pin for why it lives here.
func (m *Manager) Unpin(id string) (*state.Box, error) { return m.store.SetPinned(id, false) }
