package box

import (
	"context"
	"errors"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// rebase recomputes a workspace's desired layers and, when they differ from
// the box's recorded layers, swaps them in: the container is recreated with the
// new layers and the box's durable volumes reattached unchanged. A provision
// or lockfile change therefore reaches the box on its next wake without
// touching `/workspace` or `/home/pluto` (ADR 0017, #146).
//
// With no provision there is no content-addressed layer to recompute, and when
// the resolved layers match the record it is a no-op that costs one engine
// resolve each.
func (m *Manager) rebase(ctx context.Context, box *state.Box) (*state.Box, error) {
	if box.WorkspacePath == "" || box.EngineID == "" {
		return box, nil
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		return nil, err
	}
	if project.Provision == nil {
		return box, nil
	}
	desired, err := m.boxLayers(ctx, project, box.WorkspacePath, "")
	if err != nil {
		return nil, err
	}
	if sameLayers(box.Layers, desired) {
		return box, nil
	}
	return m.swapContainer(ctx, box, project, desired)
}

// swapContainer recreates a box's container with the given layers, reattaching
// the box's durable volumes unchanged and recording the new runtime.
func (m *Manager) swapContainer(ctx context.Context, box *state.Box, project *contract.Project, layers []container.LayerRef) (*state.Box, error) {
	if err := m.engine.Destroy(ctx, box.EngineID); err != nil && !errors.Is(err, container.ErrNoContainer) {
		return nil, err
	}
	mounts := make([]container.Mount, 0, len(box.Volumes))
	for _, v := range box.Volumes {
		kind := container.VolumeKind(v.Kind)
		mounts = append(mounts, container.Mount{Volume: container.VolumeRef{Kind: kind, Name: v.Name}, Target: volumeTarget(kind)})
	}
	engineID, err := m.engine.Create(ctx, container.CreateSpec{
		BoxID:   box.ID,
		Image:   seedImage(layers),
		Layers:  layers,
		Volumes: mounts,
		Engine:  engineCapability(project.Docker),
	})
	if err != nil {
		return nil, err
	}
	return m.store.SetBoxRuntime(box.ID, engineID, layerRecords(layers), box.Volumes)
}

// recreateIfMissing recreates a box's container from its recorded layers and
// volumes when the engine no longer has it — after a host reboot, for example —
// so the next start can run. A box with a live container is returned unchanged.
func (m *Manager) recreateIfMissing(ctx context.Context, box *state.Box) (*state.Box, error) {
	if _, err := m.engine.Status(ctx, box.EngineID); err == nil {
		return box, nil
	} else if !errors.Is(err, container.ErrNoContainer) {
		return nil, err
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		return nil, err
	}
	layers := make([]container.LayerRef, len(box.Layers))
	for i, l := range box.Layers {
		layers[i] = container.LayerRef{Kind: container.LayerKind(l.Kind), Key: l.Key}
	}
	return m.swapContainer(ctx, box, project, layers)
}

// sameLayers reports whether a box's recorded layers are exactly the desired
// resolved layers, in order.
func sameLayers(recorded []state.Layer, desired []container.LayerRef) bool {
	if len(recorded) != len(desired) {
		return false
	}
	for i := range recorded {
		if recorded[i].Kind != string(desired[i].Kind) || recorded[i].Key != desired[i].Key {
			return false
		}
	}
	return true
}
