package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// CLI is an Engine backed by a container-engine command line (docker or
// podman). Run is replaceable so tests can assert the exact engine commands
// without an engine installed; nil executes Bin.
type CLI struct {
	Bin string
	Run func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error)
}

// DefaultBinary returns the first supported container engine found on PATH.
func DefaultBinary() string {
	for _, bin := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(bin); err == nil {
			return bin
		}
	}
	return "docker"
}

// NewCLI returns a CLI engine. An empty bin selects DefaultBinary.
func NewCLI(bin string) *CLI {
	if bin == "" {
		bin = DefaultBinary()
	}
	return &CLI{Bin: bin}
}

func (c *CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, nil, args...)
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", c.Bin, strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// containerName is the engine handle for a box's container.
func containerName(boxID string) string {
	if boxID == "" {
		return "pluto-box"
	}
	return "pluto-" + boxID
}

func (c *CLI) Create(ctx context.Context, spec CreateSpec) (string, error) {
	name := containerName(spec.BoxID)
	args := []string{"create", "--name", name}
	if spec.Docker {
		// The in-box engine capability is opt-in; its exact isolation flags
		// are owned by the engine ticket (#149).
		args = append(args, "--privileged")
	}
	for _, m := range spec.Volumes {
		args = append(args, "--mount", "type=volume,src="+m.Volume.Name+",dst="+m.Target)
	}
	if spec.Image == "" {
		return "", errors.New("container image is required")
	}
	// A box is long-lived: keep the container alive so exec can attach.
	args = append(args, spec.Image, "sleep", "infinity")
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	if id := strings.TrimSpace(string(out)); id != "" {
		return id, nil
	}
	return name, nil
}

func (c *CLI) Start(ctx context.Context, id string) error {
	_, err := c.run(ctx, "start", id)
	return err
}

func (c *CLI) Stop(ctx context.Context, id string) error {
	_, err := c.run(ctx, "stop", id)
	return err
}

func (c *CLI) Freeze(ctx context.Context, id string) error {
	_, err := c.run(ctx, "pause", id)
	return err
}

func (c *CLI) Thaw(ctx context.Context, id string) error {
	_, err := c.run(ctx, "unpause", id)
	return err
}

func (c *CLI) Destroy(ctx context.Context, id string) error {
	_, err := c.run(ctx, "rm", "-f", id)
	if err != nil && isNotFound(err) {
		return ErrNoContainer
	}
	return err
}

func (c *CLI) Exec(ctx context.Context, id string, spec ExecSpec, sink io.Writer) (ExecResult, error) {
	args := []string{"exec", "-i"}
	if spec.TTY {
		args = append(args, "-t")
	}
	if spec.Dir != "" {
		args = append(args, "-w", spec.Dir)
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	args = append(args, id)
	args = append(args, spec.Argv...)

	if c.Run != nil {
		out, err := c.Run(ctx, spec.Stdin, args...)
		if err != nil {
			return ExecResult{ExitCode: exitCode(err)}, err
		}
		if sink != nil {
			_, _ = sink.Write(out)
		}
		return ExecResult{ExitCode: 0}, nil
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Stdin = spec.Stdin
	cmd.Stdout = sink
	cmd.Stderr = sink
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// The command ran and failed; that is a result, not an engine error.
			return ExecResult{ExitCode: exit.ExitCode()}, nil
		}
		return ExecResult{}, fmt.Errorf("%s exec: %w", c.Bin, err)
	}
	return ExecResult{ExitCode: 0}, nil
}

// ResolveLayer reports whether a content-addressed layer is already present. A
// layer with a digest resolves by its tagged image; a base with an OCI source
// resolves by that reference's image id. A miss is ErrNoLayer.
func (c *CLI) ResolveLayer(ctx context.Context, spec LayerSpec) (LayerRef, error) {
	if spec.Digest != "" {
		tag := layerTag(spec.Digest)
		if _, err := c.run(ctx, "image", "inspect", "--format", "{{.Id}}", tag); err != nil {
			return LayerRef{}, ErrNoLayer
		}
		return LayerRef{Kind: spec.Kind, Key: tag}, nil
	}
	if spec.Source == "" {
		return LayerRef{}, ErrNoLayer
	}
	out, err := c.run(ctx, "image", "inspect", "--format", "{{.Id}}", spec.Source)
	if err != nil {
		return LayerRef{}, ErrNoLayer
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		key = spec.Source
	}
	return LayerRef{Kind: spec.Kind, Key: key}, nil
}

// BuildLayer materializes a layer the engine does not have. A base layer with
// an OCI source is pulled; a layer with a build recipe is built into an image
// tagged by its content digest.
func (c *CLI) BuildLayer(ctx context.Context, spec LayerSpec) (LayerRef, error) {
	if spec.Build != nil {
		return c.buildImage(ctx, spec)
	}
	if spec.Kind != LayerBase {
		return LayerRef{}, fmt.Errorf("%w: %s", ErrUnsupportedLayer, spec.Kind)
	}
	if spec.Source == "" {
		return LayerRef{}, errors.New("base layer source is required")
	}
	if _, err := c.run(ctx, "pull", "--quiet", spec.Source); err != nil {
		return LayerRef{}, err
	}
	return c.ResolveLayer(ctx, spec)
}

// buildImage builds a layer's image from its recipe and tags it by the layer's
// content digest. An inline recipe is written to a temporary Dockerfile in a
// throwaway build context.
func (c *CLI) buildImage(ctx context.Context, spec LayerSpec) (LayerRef, error) {
	if spec.Digest == "" {
		return LayerRef{}, errors.New("a built layer requires a content digest")
	}
	tag := layerTag(spec.Digest)
	args := []string{"build", "-t", tag}
	dockerfile := spec.Build.Dockerfile
	contextDir := spec.Build.Context
	switch {
	case dockerfile != "":
		if contextDir == "" {
			contextDir = filepath.Dir(dockerfile)
		}
	case len(spec.Build.Run) > 0:
		dir, err := os.MkdirTemp("", "pluto-layer-")
		if err != nil {
			return LayerRef{}, err
		}
		defer os.RemoveAll(dir)
		var b strings.Builder
		if spec.Build.From != "" {
			fmt.Fprintf(&b, "FROM %s\n", spec.Build.From)
		}
		for _, step := range spec.Build.Run {
			fmt.Fprintf(&b, "RUN %s\n", step)
		}
		dockerfile = filepath.Join(dir, "Dockerfile")
		if err := os.WriteFile(dockerfile, []byte(b.String()), 0o644); err != nil {
			return LayerRef{}, err
		}
		contextDir = dir
	default:
		return LayerRef{}, fmt.Errorf("%w: %s", ErrUnsupportedLayer, spec.Kind)
	}
	args = append(args, "-f", dockerfile)
	if contextDir != "" {
		args = append(args, contextDir)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return LayerRef{}, err
	}
	return LayerRef{Kind: spec.Kind, Key: tag}, nil
}

func (c *CLI) EnsureVolume(ctx context.Context, spec VolumeSpec) (VolumeRef, error) {
	ref := VolumeRef{Kind: spec.Kind, Name: volumeName(spec)}
	if _, err := c.run(ctx, "volume", "create", ref.Name); err != nil {
		return VolumeRef{}, err
	}
	return ref, nil
}

func (c *CLI) RemoveVolume(ctx context.Context, ref VolumeRef) error {
	_, err := c.run(ctx, "volume", "rm", ref.Name)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func (c *CLI) Status(ctx context.Context, id string) (Status, error) {
	out, err := c.run(ctx, "inspect", "--format", "{{.State.Status}}", id)
	if err != nil {
		if isNotFound(err) {
			return Status{}, ErrNoContainer
		}
		return Status{}, err
	}
	return Status{ID: id, State: engineState(strings.TrimSpace(string(out)))}, nil
}

// Reconcile reports the engine's live view; a missing container is reported as
// ErrNoContainer so the caller can decide whether its record is stale.
func (c *CLI) Reconcile(ctx context.Context, id string) (Status, error) {
	return c.Status(ctx, id)
}

// engineState maps a docker/podman state string to the seam's state.
func engineState(s string) State {
	switch s {
	case "running":
		return StateRunning
	case "paused":
		return StateFrozen
	case "created":
		return StateCreated
	default: // exited, dead, removing, restarting, unknown
		return StateStopped
	}
}

func isNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") || strings.Contains(msg, "no such volume") || strings.Contains(msg, "not found")
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 1
}

var _ Engine = (*CLI)(nil)
