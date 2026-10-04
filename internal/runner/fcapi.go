package runner

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// sendCtrlAltDel asks the guest to shut down through Firecracker's API. The
// guest's systemd performs a clean stop, then the kernel resets; Firecracker
// exits on a guest reset, which stops the unit. A guest that does not act on
// it is stopped by the caller's bounded force fallback.
func sendCtrlAltDel(socketPath string) error {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPut, "http://localhost/actions", strings.NewReader(`{"action_type":"SendCtrlAltDel"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("firecracker api: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
