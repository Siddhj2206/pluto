// Package box owns the box lifecycle: it resolves a workspace through the vcs
// adapter, drives the container engine through its seam, and records the box's
// workspace, layers, volumes, and state. The core models only repo, workspace,
// layer, volume, box, task, and run; it never names a branch or worktree
// (ADR 0015).
package box

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// DefaultImage seeds a box when its request names no base image. It is
// content-addressed by the engine once resolved.
const DefaultImage = "ubuntu:24.04"

// Manager owns the workspace-to-box lifecycle on one host.
type Manager struct {
	store   *state.Store
	engine  container.Engine
	adapter vcs.Adapter
	// gate serialises work on each box by lock policy (ADR 0018).
	gate *Gate
	// services keeps each box's declared service tasks running for its life.
	services *serviceSupervisor
}

// New builds a manager around a store, a container engine, and a vcs adapter.
func New(store *state.Store, engine container.Engine, adapter vcs.Adapter) *Manager {
	return &Manager{store: store, engine: engine, adapter: adapter, gate: NewGate(), services: newServiceSupervisor(engine)}
}

// CreateRequest names the workspace and the base image to seed it from.
type CreateRequest struct {
	Workspace string
	Image     string
}

// Ensure resolves a workspace through the vcs adapter and returns its one
// durable box, creating, seeding, and starting it on first use. The bool
// reports whether a new box was created.
func (m *Manager) Ensure(ctx context.Context, req CreateRequest) (*state.Box, bool, error) {
	if m.adapter == nil || m.engine == nil {
		return nil, false, errors.New("box lifecycle is not configured")
	}
	ws, rev, err := m.adapter.Resolve(ctx, req.Workspace)
	if err != nil {
		return nil, false, err
	}
	repo := ws.Remote
	if repo == "" {
		repo = ws.Path
	}
	workspace, err := m.store.EnsureWorkspace(ws.Path, repo)
	if err != nil {
		return nil, false, err
	}
	if workspace.BoxID != "" {
		box, err := m.store.Box(workspace.BoxID)
		if err != nil {
			return nil, false, err
		}
		box, err = m.start(ctx, box)
		return box, false, err
	}

	box, created, err := m.store.CreateContainerBox(ws.Path, repo, rev.Commit)
	if err != nil {
		return nil, false, err
	}
	if !created {
		// A box already existed for this path but the workspace did not point
		// at it (a record written before workspaces); repair the link.
		if err := m.store.SetWorkspaceBox(workspace.ID, box.ID); err != nil {
			return nil, false, err
		}
		box, err = m.start(ctx, box)
		return box, false, err
	}

	project, err := contract.LoadProject(ws.Path)
	if err != nil {
		return nil, false, err
	}
	layers, err := m.boxLayers(ctx, project, ws.Path, req.Image)
	if err != nil {
		return nil, false, err
	}
	mounts := make([]container.Mount, 0, 2)
	volumeRecords := make([]state.Volume, 0, 2)
	for _, kind := range []container.VolumeKind{container.VolumeWorkspace, container.VolumeHome} {
		ref, err := m.engine.EnsureVolume(ctx, container.VolumeSpec{Kind: kind, BoxID: box.ID})
		if err != nil {
			return nil, false, err
		}
		mounts = append(mounts, container.Mount{Volume: ref, Target: volumeTarget(kind)})
		volumeRecords = append(volumeRecords, state.Volume{Kind: string(ref.Kind), Name: ref.Name})
	}
	engineID, err := m.engine.Create(ctx, container.CreateSpec{
		BoxID:   box.ID,
		Image:   seedImage(layers),
		Layers:  layers,
		Volumes: mounts,
		Engine:  engineCapability(project.Docker),
	})
	if err != nil {
		return nil, false, err
	}
	box, err = m.store.SetBoxRuntime(box.ID, engineID, layerRecords(layers), volumeRecords)
	if err != nil {
		return nil, false, err
	}
	if err := m.store.SetWorkspaceBox(workspace.ID, box.ID); err != nil {
		return nil, false, err
	}
	box, err = m.start(ctx, box)
	return box, true, err
}

// Start starts a stopped box, or returns a running one unchanged.
func (m *Manager) Start(ctx context.Context, id string) (*state.Box, error) {
	box, err := m.store.Box(id)
	if err != nil {
		return nil, err
	}
	return m.start(ctx, box)
}

// Stop stops a box, keeping its record and volumes.
func (m *Manager) Stop(ctx context.Context, id string) (*state.Box, error) {
	box, err := m.store.Box(id)
	if err != nil {
		return nil, err
	}
	m.services.stop(id)
	if box.State == state.StateStopped {
		return box, nil
	}
	if box.EngineID != "" {
		if err := m.engine.Stop(ctx, box.EngineID); err != nil {
			return nil, err
		}
	}
	return m.store.Transition(box.ID, state.StateStopped)
}

// Status reports a box, reconciling its recorded state with the engine's view.
func (m *Manager) Status(ctx context.Context, id string) (*state.Box, error) {
	box, err := m.store.Box(id)
	if err != nil {
		return nil, err
	}
	if box.EngineID == "" {
		return box, nil
	}
	// A wake_failed box's container is up but the record is authoritative:
	// its wake phase failed and it awaits a retry or removal. Do not
	// reconcile it back to running (wake_failed -> running is a legal state
	// transition, so the engine's running view would otherwise resurrect it
	// and erase the failure).
	if box.State == state.StateWakeFailed {
		return box, nil
	}
	status, err := m.engine.Reconcile(ctx, box.EngineID)
	if errors.Is(err, container.ErrNoContainer) {
		return box, nil
	}
	if err != nil {
		return nil, err
	}
	next := boxState(status.State)
	if next == "" || box.State == next {
		return box, nil
	}
	reconciled, err := m.store.Transition(box.ID, next)
	if err != nil {
		// The engine's view is not a legal transition from the record; the
		// record is the safer thing to report than a half-applied state.
		return box, nil
	}
	return reconciled, nil
}

// Remove destroys a box's container and durable volumes, then its record.
func (m *Manager) Remove(ctx context.Context, id string) error {
	box, err := m.store.Box(id)
	if err != nil {
		return err
	}
	m.services.stop(id)
	if box.EngineID != "" {
		if err := m.engine.Destroy(ctx, box.EngineID); err != nil && !errors.Is(err, container.ErrNoContainer) {
			return err
		}
	}
	for _, v := range box.Volumes {
		ref := container.VolumeRef{Kind: container.VolumeKind(v.Kind), Name: v.Name}
		if err := m.engine.RemoveVolume(ctx, ref); err != nil {
			return err
		}
	}
	return m.store.DestroyBox(id)
}

// List returns every box the host knows.
func (m *Manager) List(_ context.Context) ([]*state.Box, error) {
	boxes, _, err := m.store.Boxes()
	return boxes, err
}

// Exec runs a command in a box with a byte stream.
func (m *Manager) Exec(ctx context.Context, id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
	box, err := m.store.Box(id)
	if err != nil {
		return container.ExecResult{}, err
	}
	if box.EngineID == "" {
		return container.ExecResult{}, fmt.Errorf("box %s has no container", state.ShortID(box.ID))
	}
	return m.engine.Exec(ctx, box.EngineID, spec, sink)
}

// Run executes a command in a box under the task's lock policy: a shared run
// proceeds alongside other shared work, while an exclusive run waits for shared
// work to drain and holds the box alone. It blocks until the policy admits the
// run or ctx is cancelled, then streams the command's output to sink.
func (m *Manager) Run(ctx context.Context, id string, lock contract.Lock, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
	release, err := m.gate.Hold(ctx, id, lock)
	if err != nil {
		return container.ExecResult{}, err
	}
	defer release()
	return m.Exec(ctx, id, spec, sink)
}

// AcquireShared holds a box as a shared holder until release is called. It is
// the seam a long-lived interactive session — an attach terminal (#144) —
// registers through, so such sessions run concurrently with shared runs and
// wait while an exclusive run holds the box.
func (m *Manager) AcquireShared(ctx context.Context, id string) (func(), error) {
	return m.gate.Hold(ctx, id, contract.LockShared)
}

// start brings a box's container up and records the running state. It runs the
// wake phase on every start (first use, a start after a stop, a retry after a
// wake failure) and launches the workspace's declared service tasks afresh. A
// frozen box is thawed transparently instead: its memory is preserved, so no
// wake phase runs.
//
// A running or frozen record can outlive its container — after a host reboot the
// engine has none while the record remains — so start asks the engine first. A
// missing container is a fresh start: the container is recreated from the
// recorded layers and volumes and the wake phase runs.
func (m *Manager) start(ctx context.Context, box *state.Box) (*state.Box, error) {
	if box.EngineID == "" {
		return nil, fmt.Errorf("box %s has no container", state.ShortID(box.ID))
	}
	if box.State == state.StateRunning || box.State == state.StateFrozen {
		_, err := m.engine.Status(ctx, box.EngineID)
		switch {
		case err == nil:
			if box.State == state.StateFrozen {
				return m.thaw(ctx, box)
			}
			return box, nil
		case !errors.Is(err, container.ErrNoContainer):
			return nil, err
		}
		// The container is gone: fall through to a fresh start.
	}
	// Waking recomputes the layers and swaps them in beneath the volumes when
	// the provision or watched lockfiles changed (ADR 0017), recreating the
	// container when the engine no longer has it.
	box, err := m.rebase(ctx, box)
	if err != nil {
		return nil, err
	}
	box, err = m.recreateIfMissing(ctx, box)
	if err != nil {
		return nil, err
	}
	if err := m.engine.Start(ctx, box.EngineID); err != nil {
		return nil, err
	}
	started, err := m.store.Transition(box.ID, state.StateRunning)
	if err != nil {
		return nil, err
	}
	started, err = m.runWake(ctx, started)
	if err != nil {
		return nil, err
	}
	if err := m.startServices(started); err != nil {
		return nil, err
	}
	return started, nil
}

// thaw resumes a frozen box, preserving its memory. Thawing is transparent:
// the box returns to running without running the wake phase.
func (m *Manager) thaw(ctx context.Context, box *state.Box) (*state.Box, error) {
	if box.EngineID == "" {
		return nil, fmt.Errorf("box %s has no container", state.ShortID(box.ID))
	}
	if err := m.engine.Thaw(ctx, box.EngineID); err != nil {
		return nil, err
	}
	return m.store.Transition(box.ID, state.StateRunning)
}

// startServices launches the workspace's declared service tasks for a box that
// just started. A legacy worktree box (no workspace) declares none.
func (m *Manager) startServices(box *state.Box) error {
	if box.WorkspacePath == "" {
		return nil
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		return err
	}
	m.services.start(box, project.ServiceTasks())
	return nil
}

// resolveLayer resolves a layer, building it on a miss.
func (m *Manager) resolveLayer(ctx context.Context, spec container.LayerSpec) (container.LayerRef, error) {
	ref, err := m.engine.ResolveLayer(ctx, spec)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, container.ErrNoLayer) {
		return container.LayerRef{}, err
	}
	return m.engine.BuildLayer(ctx, spec)
}

func volumeTarget(kind container.VolumeKind) string {
	switch kind {
	case container.VolumeWorkspace:
		return "/workspace"
	case container.VolumeHome:
		return "/home/pluto"
	default:
		return "/" + string(kind)
	}
}

// engineCapability maps the contract's `docker` node to the create-time
// engine request. The zero value has no engine; privileged is the contract's
// explicit opt-in escalation.
func engineCapability(d contract.Docker) container.EngineCapability {
	return container.EngineCapability{Enabled: d.Enabled, Privileged: d.Privileged}
}

// boxState maps an engine state to a recorded box state.
func boxState(st container.State) state.BoxState {
	switch st {
	case container.StateRunning:
		return state.StateRunning
	case container.StateFrozen:
		return state.StateFrozen
	case container.StateStopped:
		return state.StateStopped
	default:
		return ""
	}
}
