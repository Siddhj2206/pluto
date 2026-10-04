// Package vsock dials guest ports through a Firecracker vsock UDS.
//
// Firecracker maps guest AF_VSOCK ports to host Unix sockets: the client
// connects to the UDS, sends "CONNECT <port>\n", and reads "OK <port>\n"
// before the connection is bridged to the guest.
// https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
package vsock

import (
	"fmt"
	"net"
	"strings"
)

// Connect dials a guest port through the multiplexer at udsPath.
func Connect(udsPath string, port uint32) (net.Conn, error) {
	conn, err := net.Dial("unix", udsPath)
	if err != nil {
		return nil, fmt.Errorf("dial vsock uds: %w", err)
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
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, fmt.Errorf("vsock connect refused: %q", line)
	}
	return conn, nil
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
