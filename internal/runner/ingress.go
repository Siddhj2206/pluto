package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/state"
)

// ForwardService owns a loopback listener and bridges each accepted connection
// to exactly one port in the selected box through SSH over vsock.
func (r *Runner) ForwardService(ctx context.Context, box *state.Box, guestPort int) (int, func(), error) {
	if guestPort <= 0 || guestPort > 65535 || guestPort == guestPortSSH {
		return 0, nil, errors.New("invalid public service port")
	}
	info, err := r.Attach(ctx, box)
	if err != nil {
		return 0, nil, errors.New("could not prepare selected box service")
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return 0, nil, errors.New("OpenSSH client is required for selected service forwarding")
	}
	vsockPath, err := exec.LookPath("pluto-vsock")
	if err != nil {
		return 0, nil, errors.New("pluto-vsock helper is required for selected service forwarding")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, errors.New("could not create a loopback listener for selected service")
	}
	forwardCtx, cancel := context.WithCancel(context.Background())
	fwd := &serviceForward{listener: listener, cancel: cancel, active: make(map[net.Conn]struct{})}
	go fwd.acceptLoop(forwardCtx, sshPath, vsockPath, info, guestPort)
	return listener.Addr().(*net.TCPAddr).Port, fwd.Close, nil
}

const guestPortSSH = 22

type serviceForward struct {
	listener net.Listener
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
	once     sync.Once
}

func (f *serviceForward) acceptLoop(ctx context.Context, sshPath, vsockPath string, info api.AttachInfo, guestPort int) {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			_ = conn.Close()
			return
		}
		f.active[conn] = struct{}{}
		f.wg.Add(1)
		f.mu.Unlock()
		go func() {
			defer func() {
				_ = conn.Close()
				f.mu.Lock()
				delete(f.active, conn)
				f.mu.Unlock()
				f.wg.Done()
			}()
			args := sshDirectArgs(vsockPath, info, guestPort)
			cmd := exec.CommandContext(ctx, sshPath, args...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = conn, conn, io.Discard
			if cmd.Start() == nil {
				_ = cmd.Wait()
			}
		}()
	}
}

func (f *serviceForward) Close() {
	f.once.Do(func() {
		f.cancel()
		_ = f.listener.Close()
		f.mu.Lock()
		f.closed = true
		for conn := range f.active {
			_ = conn.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
}

func sshDirectArgs(vsockPath string, info api.AttachInfo, guestPort int) []string {
	proxy := shellQuote(vsockPath) + " connect " + shellQuote(info.UDS) + " " + strconv.FormatUint(uint64(info.Port), 10)
	return []string{
		"-F", "/dev/null",
		"-o", "ProxyCommand=" + proxy,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-i", info.Key,
		"-W", fmt.Sprintf("127.0.0.1:%d", guestPort),
		"-p", strconv.FormatUint(uint64(info.Port), 10),
		info.User + "@pluto-box",
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
