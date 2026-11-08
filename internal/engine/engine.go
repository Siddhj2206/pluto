// Package engine runs boxes through a container engine's CLI. Everything Pluto
// knows about a specific engine stays behind the Engine interface.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// callTimeout bounds ordinary engine commands, so a hung engine can't stall the host.
	callTimeout = 2 * time.Minute
	// pullTimeout bounds image downloads, which can be slow on a home connection.
	pullTimeout = 30 * time.Minute
	// copyTimeout bounds copying a repo's files into a box.
	copyTimeout = 10 * time.Minute
)

// LabelBox marks a container as a box Pluto owns.
const LabelBox = "pluto.box"

// LabelDurable marks a durable box. It has its own label because a durable box
// outlives any run, so the host's search for leftover boxes mustn't find it.
const LabelDurable = "pluto.durable"

// ErrNotFound means the box doesn't exist.
var ErrNotFound = errors.New("box not found")

// Spec describes a box to start.
type Spec struct {
	Name  string
	Image string
	// Env is passed by name through the engine's environment, so values stay
	// out of the engine's command line.
	Env map[string]string
	Cmd []string
	// Files are copied into CopyDir before the box starts. Copying avoids bind
	// mounts, SELinux labels and VM file sharing.
	Files   map[string][]byte
	CopyDir string
	// Durable boxes stay after their runs, and get a different label.
	Durable bool
	// Volumes maps a volume's name to where it mounts in the box.
	Volumes map[string]string
	// Privileged gives the box the access builds like BuildStream's need. Only
	// safe on a rootless engine, where it can't exceed the user's own rights.
	Privileged bool
	// Entrypoint replaces the image's, so any image can idle between runs.
	Entrypoint string
	User       string
	Workdir    string
	// Init runs a small init as the box's first process, so processes that
	// builds leave behind are reaped and the box stops cleanly.
	Init bool
}

// Engine is what the host needs from a container engine.
type Engine interface {
	Name() string
	Version(ctx context.Context) (string, error)
	HasImage(ctx context.Context, image string) (bool, error)
	Pull(ctx context.Context, image string) error
	Start(ctx context.Context, spec Spec) error
	// Running reports whether the box is running, or ErrNotFound if it's gone.
	Running(ctx context.Context, name string) (bool, error)
	Exec(ctx context.Context, name string, argv ...string) ([]byte, error)
	Resume(ctx context.Context, name string) error
	Remove(ctx context.Context, name string) error
	List(ctx context.Context) ([]string, error)
	// ListDurable lists durable boxes, which List leaves out.
	ListDurable(ctx context.Context) ([]string, error)
	// EnterArgv is the command that opens a shell in a box.
	EnterArgv(name, workdir string) []string
	// Rootless reports whether the engine runs without root on the host.
	Rootless(ctx context.Context) (bool, error)
	// Stop stops a box and everything still running in it. A box that's already
	// stopped or gone is not an error.
	Stop(ctx context.Context, name string) error
	// CopyFiles writes files into a directory of a running box.
	CopyFiles(ctx context.Context, name, dir string, files map[string][]byte) error
	// CopyTar unpacks a tar stream into a directory of a running box.
	CopyTar(ctx context.Context, name, dir string, tar io.Reader) error
	// ExecDetached starts a command in a box and returns without waiting, and it
	// keeps running if the host stops. Env is passed by name, so values stay out
	// of the engine's command line.
	ExecDetached(ctx context.Context, name string, env map[string]string, argv ...string) error
	// ImageVolumes lists the paths an image declares as VOLUME.
	ImageVolumes(ctx context.Context, image string) ([]string, error)
	// RemoveVolume deletes a volume. One that's already gone is not an error.
	RemoveVolume(ctx context.Context, name string) error
	// VolumeBytes measures a volume, or fails if this engine can't.
	VolumeBytes(ctx context.Context, name string) (uint64, error)
}

// CLI drives podman or docker, which share the commands used here.
type CLI struct{ Bin string }

// Detect finds an engine on PATH, preferring podman because it runs rootless by default.
func Detect() (*CLI, error) {
	for _, bin := range []string{"podman", "docker"} {
		if path, err := exec.LookPath(bin); err == nil {
			return &CLI{Bin: path}, nil
		}
	}
	return nil, errors.New("no container engine found: install podman")
}

// Name returns the engine's short name.
func (c *CLI) Name() string { return filepath.Base(c.Bin) }

func (c *CLI) run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	return c.runTimeout(ctx, callTimeout, env, args...)
}

func (c *CLI) runTimeout(ctx context.Context, timeout time.Duration, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.WaitDelay = 5 * time.Second
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %s", c.Name(), args[0], msg)
	}
	return stdout.Bytes(), nil
}

// Version returns the engine's version.
func (c *CLI) Version(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, "version", "--format", "{{.Client.Version}}")
	return strings.TrimSpace(string(out)), err
}

// StoragePath returns where the engine keeps images and containers, which is
// the disk that decides whether a new box fits.
func (c *CLI) StoragePath(ctx context.Context) (string, error) {
	format := "{{.Store.GraphRoot}}"
	if c.Name() == "docker" {
		format = "{{.DockerRootDir}}"
	}
	out, err := c.run(ctx, nil, "info", "--format", format)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// HasImage reports whether the image is already present.
func (c *CLI) HasImage(ctx context.Context, image string) (bool, error) {
	out, err := c.run(ctx, nil, "images", "-q", image)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// Pull downloads the image, or refreshes it.
func (c *CLI) Pull(ctx context.Context, image string) error {
	_, err := c.runTimeout(ctx, pullTimeout, nil, "pull", "-q", image)
	return err
}

// Start creates a box, copies its files in, and starts it.
func (c *CLI) Start(ctx context.Context, spec Spec) error {
	label := LabelBox
	if spec.Durable {
		label = LabelDurable
	}
	args := []string{"create", "--name", spec.Name, "--label", label + "=1"}
	var env []string
	for k, v := range spec.Env {
		args = append(args, "--env", k)
		env = append(env, k+"="+v)
	}
	for _, volume := range slices.Sorted(maps.Keys(spec.Volumes)) {
		args = append(args, "--volume", volume+":"+spec.Volumes[volume])
	}
	if spec.Privileged {
		args = append(args, "--privileged", "--device", "/dev/fuse")
	}
	if spec.Init {
		args = append(args, "--init")
	}
	if spec.Entrypoint != "" {
		args = append(args, "--entrypoint", spec.Entrypoint)
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Cmd...)
	if _, err := c.run(ctx, env, args...); err != nil {
		return err
	}
	if err := c.copyFiles(ctx, spec); err != nil {
		_ = c.Remove(ctx, spec.Name)
		return err
	}
	if _, err := c.run(ctx, nil, "start", spec.Name); err != nil {
		_ = c.Remove(ctx, spec.Name)
		return err
	}
	return nil
}

func (c *CLI) copyFiles(ctx context.Context, spec Spec) error {
	if len(spec.Files) == 0 {
		return nil
	}
	return c.CopyFiles(ctx, spec.Name, spec.CopyDir, spec.Files)
}

// CopyFiles writes files into a directory of a box.
func (c *CLI) CopyFiles(ctx context.Context, name, dest string, files map[string][]byte) error {
	dir, err := os.MkdirTemp("", "pluto-box-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	for file, data := range files {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o755); err != nil {
			return err
		}
	}
	_, err = c.run(ctx, nil, "cp", dir, name+":"+dest)
	return err
}

// CopyTar unpacks a tar stream into a directory of a box.
func (c *CLI) CopyTar(ctx context.Context, name, dir string, tar io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, "cp", "-", name+":"+dir)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = tar
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s cp: %s", c.Name(), msg)
	}
	return nil
}

// Rootless reports whether the engine runs without root on the host.
func (c *CLI) Rootless(ctx context.Context) (bool, error) {
	if c.Name() == "docker" {
		out, err := c.run(ctx, nil, "info", "--format", "{{.SecurityOptions}}")
		return strings.Contains(string(out), "rootless"), err
	}
	out, err := c.run(ctx, nil, "info", "--format", "{{.Host.Security.Rootless}}")
	return strings.TrimSpace(string(out)) == "true", err
}

// Stop stops a box and everything running in it.
func (c *CLI) Stop(ctx context.Context, name string) error {
	_, err := c.run(ctx, nil, "stop", "-t", "2", name)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such") {
		return nil
	}
	if err != nil {
		// A box can get stuck stopping. Killing it directly usually frees it.
		if _, kerr := c.run(ctx, nil, "kill", "--signal", "KILL", name); kerr == nil {
			return nil
		}
	}
	return err
}

// ExecDetached starts a command in a box without waiting for it.
func (c *CLI) ExecDetached(ctx context.Context, name string, env map[string]string, argv ...string) error {
	args := []string{"exec", "-d"}
	var environ []string
	for _, k := range slices.Sorted(maps.Keys(env)) {
		args = append(args, "--env", k)
		environ = append(environ, k+"="+env[k])
	}
	args = append(append(args, name), argv...)
	_, err := c.run(ctx, environ, args...)
	return err
}

// ImageVolumes lists the paths an image declares as VOLUME, cleaned and sorted.
func (c *CLI) ImageVolumes(ctx context.Context, image string) ([]string, error) {
	out, err := c.run(ctx, nil, "image", "inspect", "--format", "{{json .Config.Volumes}}", image)
	if err != nil {
		return nil, err
	}
	var declared map[string]json.RawMessage // "null" decodes to nil.
	if err := json.Unmarshal(bytes.TrimSpace(out), &declared); err != nil {
		return nil, fmt.Errorf("reading the volumes of %s: %w", image, err)
	}
	var paths []string
	for p := range declared {
		paths = append(paths, path.Clean(p))
	}
	slices.Sort(paths)
	return paths, nil
}

// RemoveVolume deletes a volume.
func (c *CLI) RemoveVolume(ctx context.Context, name string) error {
	_, err := c.run(ctx, nil, "volume", "rm", name)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such") {
		return nil
	}
	return err
}

// VolumeBytes measures a volume. Rootless podman's files can belong to mapped
// users, so du runs inside podman's user namespace. Other engines keep volumes
// where the user can't read them.
func (c *CLI) VolumeBytes(ctx context.Context, name string) (uint64, error) {
	if c.Name() != "podman" {
		return 0, errors.New("can't measure volumes with " + c.Name())
	}
	out, err := c.run(ctx, nil, "volume", "inspect", "--format", "{{.Mountpoint}}", name)
	if err != nil {
		return 0, err
	}
	out, err = c.run(ctx, nil, "unshare", "du", "-sb", strings.TrimSpace(string(out)))
	if err != nil {
		return 0, err
	}
	size, _, _ := strings.Cut(string(out), "\t")
	return strconv.ParseUint(strings.TrimSpace(size), 10, 64)
}

// Running reports whether the box is running.
func (c *CLI) Running(ctx context.Context, name string) (bool, error) {
	out, err := c.run(ctx, nil, "inspect", "--type", "container", "--format", "{{.State.Running}}", name)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such") {
			return false, ErrNotFound
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// Exec runs a command in a running box and returns its output.
func (c *CLI) Exec(ctx context.Context, name string, argv ...string) ([]byte, error) {
	return c.run(ctx, nil, append([]string{"exec", name}, argv...)...)
}

// Resume starts a stopped box again.
func (c *CLI) Resume(ctx context.Context, name string) error {
	_, err := c.run(ctx, nil, "start", name)
	return err
}

// Remove deletes a box and its anonymous volumes, which nothing else could
// reach. Named volumes stay. A box that's already gone is not an error.
func (c *CLI) Remove(ctx context.Context, name string) error {
	args := []string{"rm", "-f", "-v"}
	if c.Name() == "podman" {
		args = append(args, "-t", "1") // Boxes sleep at the end, so there's nothing to wait for. Docker has no -t.
	}
	_, err := c.run(ctx, nil, append(args, name)...)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such") {
		return nil
	}
	return err
}

// List returns the names of all boxes Pluto owns.
func (c *CLI) List(ctx context.Context) ([]string, error) {
	return c.listLabel(ctx, LabelBox)
}

// ListDurable lists durable boxes.
func (c *CLI) ListDurable(ctx context.Context) ([]string, error) {
	return c.listLabel(ctx, LabelDurable)
}

func (c *CLI) listLabel(ctx context.Context, label string) ([]string, error) {
	out, err := c.run(ctx, nil, "ps", "-a", "--filter", "label="+label+"=1", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// EnterArgv is the command that opens a shell in a box.
func (c *CLI) EnterArgv(name, workdir string) []string {
	argv := []string{c.Bin, "exec", "-it"}
	if workdir != "" {
		argv = append(argv, "-w", workdir)
	}
	// Prefer bash, but task boxes can use images that only have sh.
	return append(argv, name, "sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh")
}
