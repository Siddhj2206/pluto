// Package engine runs boxes through a container engine's CLI. Everything Pluto
// knows about a specific engine stays behind the Engine interface.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// callTimeout bounds ordinary engine commands, so a hung engine can't stall the host.
	callTimeout = 2 * time.Minute
	// pullTimeout bounds image downloads, which can be slow on a home connection.
	pullTimeout = 30 * time.Minute
)

// LabelBox marks a container as a box Pluto owns.
const LabelBox = "pluto.box"

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
	// EnterArgv is the command that opens a shell in a box.
	EnterArgv(name, workdir string) []string
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
	args := []string{"create", "--name", spec.Name, "--label", LabelBox + "=1"}
	var env []string
	for k, v := range spec.Env {
		args = append(args, "--env", k)
		env = append(env, k+"="+v)
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
	dir, err := os.MkdirTemp("", "pluto-box-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	for name, data := range spec.Files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			return err
		}
	}
	_, err = c.run(ctx, nil, "cp", dir, spec.Name+":"+spec.CopyDir)
	return err
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

// Remove deletes a box. A box that's already gone is not an error.
func (c *CLI) Remove(ctx context.Context, name string) error {
	args := []string{"rm", "-f"}
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
	out, err := c.run(ctx, nil, "ps", "-a", "--filter", "label="+LabelBox+"=1", "--format", "{{.Names}}")
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
	return append(argv, name, "bash")
}
