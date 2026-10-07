package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/shquote"
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
	plutoPath := r.Exe
	if plutoPath == "" {
		plutoPath, err = exec.LookPath("pluto")
		if err != nil {
			return 0, nil, errors.New("Pluto executable is required for selected service forwarding")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, errors.New("could not create a loopback listener for selected service")
	}
	forwardCtx, cancel := context.WithCancel(context.Background())
	fwd := &serviceForward{listener: listener, cancel: cancel, active: make(map[net.Conn]struct{})}
	go fwd.acceptLoop(forwardCtx, sshPath, plutoPath, info, guestPort)
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

func (f *serviceForward) acceptLoop(ctx context.Context, sshPath, plutoPath string, info api.AttachInfo, guestPort int) {
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
			args := sshDirectArgs(plutoPath, info, guestPort)
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

func sshDirectArgs(plutoPath string, info api.AttachInfo, guestPort int) []string {
	proxy := shquote.Quote(plutoPath) + " vsock connect " + shquote.Quote(info.UDS) + " " + strconv.FormatUint(uint64(info.Port), 10)
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
