// Package container is the redrawn runner seam: one container backend behind a
// single interface, in container terms rather than in any prior substrate's
// terms (ADR 0014). The daemon drives lifecycle, exec, layers, and volumes
// through it; a fake lets tests drive the same verbs deterministically.
package container

import (
	"context"
	"errors"
	"io"
)

// LayerKind distinguishes the two tiers a box's root is composed from.
type LayerKind string

const (
	LayerBase    LayerKind = "base"
	LayerPackage LayerKind = "package"
)

// LayerSpec describes how to produce a layer. Source is the content origin: an
// OCI reference for a base layer, or the parent key for a package layer, whose
// Digest is the content fingerprint (for example a watched lockfile hash).
//
// A layer with a non-empty Digest is content-addressed by that fingerprint and
// built from Build when the engine does not already have it. A base layer with
// no Digest and a Source is pulled from that OCI reference.
type LayerSpec struct {
	Kind   LayerKind
	Source string
	Parent string
	Digest string
	Build  *LayerBuild
}

// LayerBuild is the recipe for building a layer's image rather than pulling
// one. Exactly one of Dockerfile or Run is set.
type LayerBuild struct {
	// From is the image the build starts from. Empty means a Dockerfile
	// names its own base.
	From string
	// Dockerfile is the path to an existing Dockerfile.
	Dockerfile string
	// Context is the build context directory; it defaults to the
	// Dockerfile's directory when Dockerfile is set.
	Context string
	// Run are the shell steps to run in the build, in order.
	Run []string
}

// layerTag names the image a built layer is tagged with. The tag is derived
// from the content digest, so the same content resolves to the same image.
func layerTag(digest string) string {
	return "pluto-layer:" + digest
}

// LayerRef is a resolved, content-addressed layer.
type LayerRef struct {
	Kind LayerKind `json:"kind"`
	Key  string    `json:"key"`
}

// VolumeKind distinguishes the durable volumes mounted outside the overlay.
type VolumeKind string

const (
	VolumeWorkspace VolumeKind = "workspace"
	VolumeHome      VolumeKind = "home"
)

// VolumeSpec names the volume to ensure.
type VolumeSpec struct {
	Kind  VolumeKind
	BoxID string
}

// VolumeRef is an ensured volume.
type VolumeRef struct {
	Kind VolumeKind `json:"kind"`
	Name string     `json:"name"`
}

// Mount binds a volume into a box at a target path.
type Mount struct {
	Volume VolumeRef `json:"volume"`
	Target string    `json:"target"`
}

// State is the container's observed lifecycle state.
type State string

const (
	StateCreated State = "created"
	StateRunning State = "running"
	StateFrozen  State = "frozen"
	StateStopped State = "stopped"
)

// CreateSpec is the shape of the container to create for a box.
type CreateSpec struct {
	BoxID   string     `json:"box_id"`
	Image   string     `json:"image"`
	Layers  []LayerRef `json:"layers,omitempty"`
	Volumes []Mount    `json:"volumes,omitempty"`
	// Engine requests the box's own in-box container engine (#149): a nested
	// engine isolated from the host's. The zero value is no engine.
	Engine EngineCapability `json:"engine,omitempty"`
}

// EngineCapability is the box's in-box container-engine request. The default,
// Enabled without Privileged, runs a rootless engine: it never mounts the host
// engine's socket and never escalates privilege. Privileged opts into the
// escalation some full builds need and widens the isolation boundary; see
// docs/adr/0019.
type EngineCapability struct {
	Enabled    bool `json:"enabled,omitempty"`
	Privileged bool `json:"privileged,omitempty"`
}

// ExecSpec is one command to run inside a box, with an optional byte stream.
type ExecSpec struct {
	Argv  []string
	Dir   string
	Env   map[string]string
	TTY   bool
	Stdin io.Reader
}

// ExecResult is the outcome of an exec.
type ExecResult struct {
	ExitCode int `json:"exit_code"`
}

// Status is a container's observed identity and state.
type Status struct {
	ID    string `json:"id"`
	State State  `json:"state"`
}

// ErrNoLayer reports that a layer is not present.
var ErrNoLayer = errors.New("layer not found")

// ErrNoContainer reports that a container is not present.
var ErrNoContainer = errors.New("container not found")

// ErrUnsupportedLayer reports a layer kind the engine cannot build yet.
var ErrUnsupportedLayer = errors.New("layer build is not supported for this kind")

// Engine is the redrawn runner seam. Lifecycle verbs create, start, stop,
// freeze, thaw, and destroy a box's container; Exec runs a command with a byte
// stream (tty or not); ResolveLayer and BuildLayer produce the content-addressed
// layers a box's root is composed from; EnsureVolume and RemoveVolume manage the
// durable volumes outside that stack; Status and Reconcile report and repair the
// observed state.
type Engine interface {
	Create(ctx context.Context, spec CreateSpec) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Freeze(ctx context.Context, id string) error
	Thaw(ctx context.Context, id string) error
	Destroy(ctx context.Context, id string) error

	Exec(ctx context.Context, id string, spec ExecSpec, sink io.Writer) (ExecResult, error)

	ResolveLayer(ctx context.Context, spec LayerSpec) (LayerRef, error)
	BuildLayer(ctx context.Context, spec LayerSpec) (LayerRef, error)
	EnsureVolume(ctx context.Context, spec VolumeSpec) (VolumeRef, error)
	RemoveVolume(ctx context.Context, ref VolumeRef) error

	Status(ctx context.Context, id string) (Status, error)
	Reconcile(ctx context.Context, id string) (Status, error)
}
