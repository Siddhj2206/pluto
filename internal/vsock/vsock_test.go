package vsock_test

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"testing"

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
