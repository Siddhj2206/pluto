package vsock_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/vsock"
)

// fakeMux mimics Firecracker's vsock host-side multiplexer: it expects a
// "CONNECT <port>\n" line and replies "OK <port>\n", then bridges bytes.
func fakeMux(t *testing.T) (path string, requests chan string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	requests = make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				requests <- line
				if line == "CONNECT 22\n" {
					io.WriteString(c, "OK 1073741824\n")
					io.Copy(c, c)
					return
				}
				io.WriteString(c, "ERR invalid port\n")
			}(conn)
		}
	}()
	return path, requests
}

func TestConnectHandshakesAndBridges(t *testing.T) {
	path, requests := fakeMux(t)

	conn, err := vsock.Connect(path, 22)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	if got := <-requests; got != "CONNECT 22\n" {
		t.Fatalf("handshake = %q, want CONNECT 22", got)
	}
	if _, err := io.WriteString(conn, "hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len("hello\n"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hello\n" {
		t.Fatalf("echo = %q, want hello", buf)
	}
}

func TestConnectRefusesNonOKReply(t *testing.T) {
	path, _ := fakeMux(t)
	conn, err := vsock.Connect(path, 9999)
	if err == nil {
		conn.Close()
		t.Fatal("Connect should fail when the mux replies ERR")
	}
}

func TestConnectFailsOnMissingSocket(t *testing.T) {
	if conn, err := vsock.Connect(filepath.Join(t.TempDir(), "missing.sock"), 22); err == nil {
		conn.Close()
		t.Fatal("Connect should fail when the UDS is missing")
	}
}

func TestWaitReadyReturnsBanner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		bufio.NewReader(c).ReadString('\n')
		io.WriteString(c, "OK 3\nSSH-2.0-test\n")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	banner, err := vsock.WaitReady(ctx, path, 22)
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if banner != "SSH-2.0-test" {
		t.Fatalf("banner = %q, want SSH-2.0-test", banner)
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := vsock.WaitReady(ctx, filepath.Join(t.TempDir(), "missing.sock"), 22)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestWaitReadyIsBoundedWhenMuxStalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c // accepted but never answered: the mux stalls
	}()
	t.Cleanup(func() {
		select {
		case c := <-accepted:
			c.Close()
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = vsock.WaitReady(ctx, path, 22)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("WaitReady took %s against a stalled mux, want it bounded by the context", elapsed)
	}
}

func TestProxyBridgesGuestBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		bufio.NewReader(c).ReadString('\n')
		io.WriteString(c, "OK 3\n")
		io.WriteString(c, "pong\n")
	}()

	pr, pw := io.Pipe()
	defer pw.Close()
	var out bytes.Buffer
	if err := vsock.Proxy(path, 22, pr, &out); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if out.String() != "pong\n" {
		t.Fatalf("out = %q, want pong", out.String())
	}
}

func TestConnectRejectsMalformedOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		bufio.NewReader(c).ReadString('\n')
		io.WriteString(c, "OKAY garbage\n")
	}()
	if conn, err := vsock.Connect(path, 22); err == nil {
		conn.Close()
		t.Fatal("Connect should reject a reply that is not OK <port>")
	}
}
