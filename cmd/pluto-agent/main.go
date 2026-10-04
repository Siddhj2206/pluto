// Command pluto-agent is the box-side agent: it applies the contract the
// daemon sends over vsock (provision once, wake on every start, services as
// user units) and reports phase status back.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Siddhj2206/pluto/internal/agent"
	"github.com/Siddhj2206/pluto/internal/vsock"
)

func main() {
	root := os.Getenv("PLUTO_AGENT_ROOT")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal(err)
		}
		root = filepath.Join(home, ".local", "state", "pluto")
	}
	ag, err := agent.New(root, agent.NewSystemd())
	if err != nil {
		fatal(err)
	}
	ln, err := vsock.Listen(agent.Port)
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	fmt.Fprintf(os.Stderr, "pluto-agent listening on vsock port %d (state %s)\n", agent.Port, root)
	if err := ag.Serve(ln); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "pluto-agent:", err)
	os.Exit(1)
}
