package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"
)

// Fake is the controllable Engine tests drive. It records every verb it was
// asked to perform, keeps an in-memory view of containers, layers, and
// volumes, and lets a test script failures and exec output.
type Fake struct {
	mu      sync.Mutex
	nextID  int
	states  map[string]State
	layers  map[string]LayerRef
	volumes map[string]VolumeRef
	calls   []string

	lastCreate  CreateSpec
	createdOnce bool

	// Scripted failures. A nil field succeeds.
	CreateErr  error
	StartErr   error
	StopErr    error
	FreezeErr  error
	ThawErr    error
	DestroyErr error
	ExecErr    error
	StatusErr  error

	// ExecFunc, when set, replaces the default no-op exec (which writes
	// nothing and exits zero). It stands in for a task's byte stream.
	ExecFunc func(id string, spec ExecSpec, sink io.Writer) (ExecResult, error)
}

// NewFake returns an empty fake engine.
func NewFake() *Fake {
	return &Fake{states: map[string]State{}, layers: map[string]LayerRef{}, volumes: map[string]VolumeRef{}}
}

func (f *Fake) record(op, id string) {
	if id == "" {
		f.calls = append(f.calls, op)
		return
	}
	f.calls = append(f.calls, op+":"+id)
}

// Calls returns the recorded calls in order, each as "op" or "op:id".
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// State returns a container's recorded state.
func (f *Fake) State(id string) (State, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[id]
	return st, ok
}

// ResolvedLayers returns the layers the fake has produced or seen.
func (f *Fake) ResolvedLayers() []LayerRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]LayerRef, 0, len(f.layers))
	for _, l := range f.layers {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (f *Fake) Create(_ context.Context, spec CreateSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	f.nextID++
	id := fmt.Sprintf("fake-%d", f.nextID)
	f.states[id] = StateCreated
	f.lastCreate = spec
	f.createdOnce = true
	f.record("create", spec.BoxID)
	return id, nil
}

// LastCreateSpec returns the spec of the most recent Create, so a test can
// assert the create-time capability the manager requested.
func (f *Fake) LastCreateSpec() (CreateSpec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCreate, f.createdOnce
}

func (f *Fake) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartErr != nil {
		return f.StartErr
	}
	if _, ok := f.states[id]; !ok {
		return ErrNoContainer
	}
	f.states[id] = StateRunning
	f.record("start", id)
	return nil
}

func (f *Fake) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StopErr != nil {
		return f.StopErr
	}
	if _, ok := f.states[id]; !ok {
		return ErrNoContainer
	}
	f.states[id] = StateStopped
	f.record("stop", id)
	return nil
}

func (f *Fake) Freeze(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FreezeErr != nil {
		return f.FreezeErr
	}
	if _, ok := f.states[id]; !ok {
		return ErrNoContainer
	}
	f.states[id] = StateFrozen
	f.record("freeze", id)
	return nil
}

func (f *Fake) Thaw(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ThawErr != nil {
		return f.ThawErr
	}
	if _, ok := f.states[id]; !ok {
		return ErrNoContainer
	}
	f.states[id] = StateRunning
	f.record("thaw", id)
	return nil
}

func (f *Fake) Destroy(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DestroyErr != nil {
		return f.DestroyErr
	}
	if _, ok := f.states[id]; !ok {
		return ErrNoContainer
	}
	delete(f.states, id)
	f.record("destroy", id)
	return nil
}

func (f *Fake) Exec(_ context.Context, id string, spec ExecSpec, sink io.Writer) (ExecResult, error) {
	f.mu.Lock()
	exec := f.ExecFunc
	f.mu.Unlock()
	if _, ok := f.State(id); !ok {
		return ExecResult{}, ErrNoContainer
	}
	if f.ExecErr != nil {
		return ExecResult{}, f.ExecErr
	}
	f.record("exec", id)
	if exec != nil {
		return exec(id, spec, sink)
	}
	return ExecResult{ExitCode: 0}, nil
}

func (f *Fake) ResolveLayer(_ context.Context, spec LayerSpec) (LayerRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := layerKey(spec)
	if ref, ok := f.layers[key]; ok {
		f.record("resolve-layer", ref.Key)
		return ref, nil
	}
	f.record("resolve-layer", key)
	return LayerRef{}, ErrNoLayer
}

func (f *Fake) BuildLayer(_ context.Context, spec LayerSpec) (LayerRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := LayerRef{Kind: spec.Kind, Key: layerKey(spec)}
	f.layers[ref.Key] = ref
	f.record("build-layer", ref.Key)
	return ref, nil
}

func (f *Fake) EnsureVolume(_ context.Context, spec VolumeSpec) (VolumeRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := volumeName(spec)
	ref := VolumeRef{Kind: spec.Kind, Name: name}
	f.volumes[name] = ref
	f.record("ensure-volume", name)
	return ref, nil
}

func (f *Fake) RemoveVolume(_ context.Context, ref VolumeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.volumes, ref.Name)
	f.record("remove-volume", ref.Name)
	return nil
}

func (f *Fake) Status(_ context.Context, id string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StatusErr != nil {
		return Status{}, f.StatusErr
	}
	st, ok := f.states[id]
	if !ok {
		return Status{}, ErrNoContainer
	}
	f.record("status", id)
	return Status{ID: id, State: st}, nil
}

func (f *Fake) Reconcile(ctx context.Context, id string) (Status, error) {
	return f.Status(ctx, id)
}

// layerKey derives a deterministic content address for a layer spec.
func layerKey(spec LayerSpec) string {
	sum := sha256.Sum256([]byte(string(spec.Kind) + "\x00" + spec.Source + "\x00" + spec.Parent + "\x00" + spec.Digest))
	return string(spec.Kind) + ":" + hex.EncodeToString(sum[:])[:16]
}

// volumeName derives the deterministic volume name for a box and kind.
func volumeName(spec VolumeSpec) string {
	return "pluto-" + spec.BoxID + "-" + string(spec.Kind)
}

var _ Engine = (*Fake)(nil)
