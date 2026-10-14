package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/state"
)

// attachUsage is the one-line usage printed with an attach usage error.
const attachUsage = "usage: pluto attach [box-id|workspace] [--watch]"

// runAttach opens a terminal into a box, waking it first. The terminal is
// shared and masked: `--watch` streams it read-only and any other client may
// type.
func runAttach(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "attach", stdout) {
		return 0
	}
	target, watch, err := parseAttachArgs(args)
	if err != nil {
		return usageError(stderr, err.Error(), attachUsage)
	}
	if target == "" {
		dir, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		target = dir
	}
	c := client.New(socket)
	box, err := resolveContainerBox(c, target)
	if err != nil {
		return fail(stderr, err)
	}
	return runTerminalAttach(c, box, watch, stdout, stderr)
}

// parseAttachArgs reads attach's target and --watch.
func parseAttachArgs(args []string) (target string, watch bool, err error) {
	for _, arg := range args {
		if arg == "--watch" {
			watch = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return "", false, fmt.Errorf("unknown attach flag %q", arg)
		}
		if target != "" {
			return "", false, errors.New("attach takes one box")
		}
		target = arg
	}
	return target, watch, nil
}

// runTerminalAttach opens a box's daemon-owned terminal. The terminal is shared
// and masked; watch makes the connection read-only.
func runTerminalAttach(c *client.Client, box *state.Box, watch bool, stdout, stderr io.Writer) int {
	code, err := c.AttachTerminal(box.ID, watch, os.Stdin, stdout)
	if err != nil {
		return fail(stderr, err, fmt.Sprintf("check the box with 'pluto box status %s' and retry", short(box.ID)))
	}
	return code
}
