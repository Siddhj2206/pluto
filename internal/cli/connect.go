package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
)

const connectUsage = "usage: pluto connect [box-id|worktree] SERVICE [--access-mode ssh-tunnel] [--provider ssh] [--local-port PORT] [--json]"

func runConnect(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "connect", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accessMode := fs.String("access-mode", "ssh-tunnel", "service access route")
	provider := fs.String("provider", "ssh", "access provider")
	localPort := fs.Int("local-port", 0, "local tunnel port (default: service port)")
	jsonOutput := fs.Bool("json", false, "print the structured connection result as JSON")
	if code := parseCommand(fs, splitFlags(args, "--access-mode", "--provider", "--local-port"), stderr, connectUsage); code != 0 {
		return code
	}
	positionals := fs.Args()
	var target, service string
	switch len(positionals) {
	case 1:
		service = positionals[0]
		var err error
		target, err = os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
	case 2:
		target, service = positionals[0], positionals[1]
	default:
		return usageError(stderr, "connect requires a service and optional box target", connectUsage)
	}
	box, err := resolveBox(client.New(socket), target)
	if err != nil {
		return fail(stderr, err)
	}
	result, err := client.New(socket).ConnectService(box.ID, api.ConnectRequest{
		Service: service, AccessMode: *accessMode, Provider: *provider, LocalPort: *localPort,
	})
	if err != nil {
		return fail(stderr, err)
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	fmt.Fprintf(stdout, "service %s is ready at %s\n", result.Service, result.Endpoint)
	fmt.Fprintf(stdout, "access: %s via %s\n", result.Access.Mode, result.Access.Provider)
	fmt.Fprintf(stdout, "authentication: %s\n", result.Access.Authentication)
	for _, instruction := range result.Access.Instructions {
		fmt.Fprintf(stdout, "next: %s\n", instruction)
	}
	return 0
}
