package vsock

import (
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Listen binds an AF_VSOCK stream listener on a port. It is the guest-side
// half of the vsock story: the host reaches it through Firecracker's mux
// (Connect), while this listener serves the guest agent.
//
// The listener is hand-rolled rather than wrapped with net.FileListener:
// Go's net package cannot name AF_VSOCK sockets (getsockname reports
// "address family not supported by protocol").
func Listen(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock bind port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &vsockListener{
		file: os.NewFile(uintptr(fd), fmt.Sprintf("vsock-listener:%d", port)),
		port: port,
	}, nil
}

type vsockListener struct {
	file   *os.File
	port   uint32
	closed atomic.Bool
}

func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		if l.closed.Load() {
			return nil, net.ErrClosed
		}
		// Poll first so Accept never blocks and Close can interrupt it.
		fds := []unix.PollFd{{Fd: int32(l.file.Fd()), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 250)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			if l.closed.Load() {
				return nil, net.ErrClosed
			}
			return nil, err
		}
		if l.closed.Load() {
			return nil, net.ErrClosed
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		fd, _, err := unix.Accept4(int(l.file.Fd()), unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			if l.closed.Load() {
				return nil, net.ErrClosed
			}
			return nil, err
		}
		return &vsockConn{file: os.NewFile(uintptr(fd), "vsock-conn")}, nil
	}
}

func (l *vsockListener) Close() error {
	l.closed.Store(true)
	return l.file.Close()
}

func (l *vsockListener) Addr() net.Addr {
	return &vsockAddr{port: l.port}
}

// vsockConn adapts an accepted vsock socket to net.Conn. The fd is
// non-blocking, so os.File's poller provides blocking I/O and deadlines.
type vsockConn struct {
	file *os.File
}

func (c *vsockConn) Read(b []byte) (int, error)  { return c.file.Read(b) }
func (c *vsockConn) Write(b []byte) (int, error) { return c.file.Write(b) }
func (c *vsockConn) Close() error                { return c.file.Close() }

func (c *vsockConn) LocalAddr() net.Addr  { return vsockSockaddr(c.file, true) }
func (c *vsockConn) RemoteAddr() net.Addr { return vsockSockaddr(c.file, false) }

func (c *vsockConn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *vsockConn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }

func vsockSockaddr(file *os.File, local bool) net.Addr {
	var (
		sa  unix.Sockaddr
		err error
	)
	if local {
		sa, err = unix.Getsockname(int(file.Fd()))
	} else {
		sa, err = unix.Getpeername(int(file.Fd()))
	}
	if err != nil {
		return &vsockAddr{}
	}
	if vm, ok := sa.(*unix.SockaddrVM); ok {
		return &vsockAddr{cid: vm.CID, port: vm.Port}
	}
	return &vsockAddr{}
}

type vsockAddr struct {
	cid  uint32
	port uint32
}

func (a *vsockAddr) Network() string { return "vsock" }
func (a *vsockAddr) String() string  { return fmt.Sprintf("vsock:%d:%d", a.cid, a.port) }
