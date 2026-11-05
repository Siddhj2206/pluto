package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/Siddhj2206/pluto/internal/host"
)

// client talks to the host's API. Every command goes through it, so a later
// remote route needs no new protocol.
type client struct{ http *http.Client }

func newClient() *client {
	sock := socketPath()
	return &client{http: &http.Client{
		Timeout: 2 * time.Minute,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}}
}

// userError carries a message, the command that helps, and an exit code.
type userError struct {
	msg  string
	next string
	code int
}

func (e *userError) Error() string { return e.msg }

var errHostDown = &userError{msg: "the Pluto host isn't running.", next: "pluto host start", code: exitNoHost}

func (c *client) raw(method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://pluto"+path, r)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) {
			return nil, errHostDown
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var f host.Failure
		if json.Unmarshal(data, &f) == nil && f.Error != "" {
			return nil, &userError{msg: f.Error, next: f.Next, code: exitError}
		}
		return nil, fmt.Errorf("the host returned %s", resp.Status)
	}
	return data, nil
}

// require checks that the host has a capability, so an old host gets a clear
// message and not a confusing API error.
func (c *client) require(capability string) error {
	var info host.Info
	if err := c.json(http.MethodGet, "/v1/info", nil, &info); err != nil {
		return err
	}
	if slices.Contains(info.Capabilities, capability) {
		return nil
	}
	return &userError{
		msg:  fmt.Sprintf("the host (pluto %s) doesn't support %q.", info.Version, capability),
		next: "upgrade Pluto, then run: pluto host start",
		code: exitError,
	}
}

func (c *client) json(method, path string, body, out any) error {
	data, err := c.raw(method, path, body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
