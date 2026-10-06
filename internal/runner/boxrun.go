package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// BoxRun is the per-box unit's main process: it starts the VMM inside a
// rootless user and network namespace and supervises the slirp4netns process
// that gives the box egress. When it exits, the unit exits.
func BoxRun(ctx context.Context, root, id string) error {
	if !state.ValidID(id) {
		return fmt.Errorf("invalid box id %q", id)
	}
	boxDir := filepath.Join(root, "boxes", id)
	record, err := state.ReadBox(filepath.Join(boxDir, "box.json"))
	if err != nil {
		return fmt.Errorf("read box record: %w", err)
	}
	if record.Image == "" {
		return fmt.Errorf("box %s has no image pinned", id)
	}
	imageDir := filepath.Join(root, "images", record.Image)
	cfg := filepath.Join(boxDir, "fc.json")
	apiSock := filepath.Join(boxDir, "firecracker.sock")

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find pluto binary: %w", err)
	}
	unshare, err := findUnshare()
	if err != nil {
		return err
	}
	slirp, err := exec.LookPath("slirp4netns")
	if err != nil {
		return fmt.Errorf("slirp4netns not found in PATH: %w", err)
	}
	serialLog, err := openRotatingLog(filepath.Join(boxDir, "serial.log"), maxLogBytes)
	if err != nil {
		return err
	}
	defer serialLog.Close()
	slirpLog, err := openRotatingLog(filepath.Join(boxDir, "slirp.log"), maxLogBytes)
	if err != nil {
		return err
	}
	defer slirpLog.Close()

	// Firecracker writes its structured log to a named pipe; drain it into a
	// bounded file the runner owns. Opening the pipe read/write keeps reads
	// from reporting EOF between the runner's start and Firecracker's open.
	if err := makeFIFO(fcLogPipe(boxDir)); err != nil {
		return err
	}
	fifo, err := os.OpenFile(fcLogPipe(boxDir), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	fcLog, err := openRotatingLog(fcLogPath(boxDir), maxLogBytes)
	if err != nil {
		fifo.Close()
		return err
	}
	defer fcLog.Close()
	go func() {
		defer fifo.Close()
		_ = pumpLog(fifo, fcLog)
	}()

	holder := exec.CommandContext(ctx, unshare, "-Urn", "--", exe, "box", "holder",
		cfg, apiSock, filepath.Join(imageDir, "firecracker"))
	holder.Stdout = serialLog
	holder.Stderr = serialLog
	if err := holder.Start(); err != nil {
		return fmt.Errorf("start namespace holder: %w", err)
	}

	slirpCmd := exec.CommandContext(ctx, slirp, "--configure", "--mtu=1500", strconv.Itoa(holder.Process.Pid), tapSlirp)
	slirpCmd.Stdout = slirpLog
	slirpCmd.Stderr = slirpLog
	if err := slirpCmd.Start(); err != nil {
		_ = holder.Process.Kill()
		_ = holder.Wait()
		return fmt.Errorf("start slirp4netns: %w", err)
	}

	err = holder.Wait()
	_ = slirpCmd.Process.Kill()
	_ = slirpCmd.Wait()
	return err
}

// BoxHolder runs inside the namespace BoxRun created: it starts Firecracker,
// bridges its tap to slirp4netns's tap, and waits for the VMM.
func BoxHolder(ctx context.Context, cfgPath, apiSock, fcPath string) error {
	cmd := exec.CommandContext(ctx, fcPath, "--api-sock", apiSock, "--config-file", cfgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start firecracker: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := bridgeTaps(ctx); err != nil {
		_ = cmd.Process.Kill()
		<-done
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}

// bridgeTaps joins Firecracker's tap and slirp4netns's tap inside the
// namespace. They cannot share one device: slirp holds the fd of the tap it
// creates, so the VMM gets its own and a bridge connects them.
func bridgeTaps(ctx context.Context) error {
	if err := waitForLink(ctx, tapFc); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"link", "add", bridge, "type", "bridge"},
		{"link", "set", tapFc, "master", bridge},
		{"link", "set", tapFc, "up"},
		{"link", "set", bridge, "up"},
	} {
		if err := runIP(ctx, args...); err != nil {
			return err
		}
	}
	if err := waitForLink(ctx, tapSlirp); err != nil {
		return err
	}
	if err := runIP(ctx, "link", "set", tapSlirp, "master", bridge); err != nil {
		return err
	}
	return runIP(ctx, "link", "set", tapSlirp, "up")
}

func waitForLink(ctx context.Context, name string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := runIP(ctx, "link", "show", name); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func runIP(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "ip", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func findUnshare() (string, error) {
	if _, err := os.Stat("/usr/bin/unshare"); err == nil {
		return "/usr/bin/unshare", nil
	}
	path, err := exec.LookPath("unshare")
	if err != nil {
		return "", fmt.Errorf("unshare not found: %w", err)
	}
	return path, nil
}
