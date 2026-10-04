// Package vsock dials guest ports through a Firecracker vsock UDS.
//
// Firecracker maps guest AF_VSOCK ports to host Unix sockets: the client
// connects to the UDS, sends "CONNECT <port>\n", and reads "OK <port>\n"
// before the connection is bridged to the guest.
// https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
package vsock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Connect dials a guest port through the multiplexer at udsPath.
func Connect(udsPath string, port uint32) (net.Conn, error) {
	return connect(udsPath, port, time.Time{})
}

// connect dials with an optional deadline covering the handshake, so a
// multiplexer that accepts but never replies cannot stall the caller.
func connect(udsPath string, port uint32, deadline time.Time) (net.Conn, error) {
	conn, err := net.Dial("unix", udsPath)
	if err != nil {
		return nil, fmt.Errorf("dial vsock uds: %w", err)
	}
	if !deadline.IsZero() {
		_ = conn.SetReadDeadline(deadline)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}
	line, err := readLine(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read vsock reply: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, fmt.Errorf("vsock connect refused: %q", line)
	}
	return conn, nil
}

// WaitReady polls a guest port until it answers with a banner, or ctx is
// done. It is how the runner waits for a booting box's sshd.
func WaitReady(ctx context.Context, udsPath string, port uint32) (string, error) {
	for {
		if banner, ok := tryBanner(ctx, udsPath, port); ok {
			return banner, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Proxy bridges in and out to the guest port, for use as an ssh ProxyCommand.
func Proxy(udsPath string, port uint32, in io.Reader, out io.Writer) error {
	conn, err := Connect(udsPath, port)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		io.Copy(conn, in)
		conn.Close()
	}()
	_, err = io.Copy(out, conn)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func tryBanner(ctx context.Context, udsPath string, port uint32) (string, bool) {
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn, err := connect(udsPath, port, deadline)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if n == 0 {
		return "", false
	}
	return strings.TrimSpace(string(buf[:n])), true
}

// readLine reads through the first newline without buffering past it: the
// guest may send data immediately after the reply, and a bufio.Reader would
// swallow it.
func readLine(conn net.Conn) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		if buf[0] == '\n' {
			return string(line), nil
		}
		line = append(line, buf[0])
		if len(line) > 128 {
			return "", fmt.Errorf("reply line too long")
		}
	}
}
