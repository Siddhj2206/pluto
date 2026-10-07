package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/provider"
)

const providerUsage = "usage: pluto provider list | status [ID] | install ID [--approve] | enable ID [--approve] | disable ID | remove ID | route add BOX SERVICE --approve --confirm-auth | route remove ROUTE_ID"

func runProvider(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelpAtStart(args, "provider", stdout) {
		return 0
	}
	if len(args) == 0 {
		return usageError(stderr, "provider action is required", providerUsage)
	}
	action := args[0]
	if action == "route" {
		return runProviderRoute(args[1:], socket, stdout, stderr)
	}
	if wantsHelp(args[1:]) {
		if text, ok := commandHelp("provider " + action); ok {
			fmt.Fprint(stdout, text)
			return 0
		}
	}
	if action == "help" {
		if text, ok := commandHelp("provider"); ok {
			fmt.Fprint(stdout, text)
		}
		return 0
	}
	if action == "list" || action == "status" {
		if action == "list" && len(args) != 1 || action == "status" && len(args) > 2 {
			return usageError(stderr, "invalid provider status arguments", providerUsage)
		}
		providers, err := client.New(socket).Providers()
		if err != nil {
			return fail(stderr, err)
		}
		for _, p := range providers {
			if action == "status" && len(args) == 2 && args[1] != p.Info.ID {
				continue
			}
			printProvider(stdout, p)
		}
		if action == "status" && len(args) == 2 {
			found := false
			for _, p := range providers {
				if p.Info.ID == args[1] {
					found = true
				}
			}
			if !found {
				return fail(stderr, fmt.Errorf("unknown provider %q", args[1]))
			}
		}
		return 0
	}
	if action != "install" && action != "enable" && action != "disable" && action != "remove" {
		return unknownSubcommand(stderr, "provider", action, []string{"list", "status", "install", "enable", "disable", "remove"})
	}
	fs := flag.NewFlagSet("provider "+action, flag.ContinueOnError)
	approved := fs.Bool("approve", false, "approve this provider operation as the host owner")
	if code := parseCommand(fs, splitFlags(args[1:], "--approve"), stderr, providerUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "provider id is required", providerUsage)
	}
	id := fs.Arg(0)
	if action == "install" || action == "enable" {
		providers, err := client.New(socket).Providers()
		if err != nil {
			return fail(stderr, err)
		}
		var disclosure *api.ProviderStatus
		for i := range providers {
			if providers[i].Info.ID == id {
				disclosure = &providers[i]
				break
			}
		}
		if disclosure == nil {
			return fail(stderr, fmt.Errorf("unknown provider %q", id))
		}
		printProviderDisclosure(stdout, disclosure.Info)
		if !*approved {
			fmt.Fprintf(stderr, "pluto: %s requires explicit host-owner approval\nnext: review the disclosures and rerun 'pluto provider %s %s --approve'\n", action, action, id)
			return 1
		}
	}
	out, err := client.New(socket).ProviderAction(id, action, *approved)
	if err != nil {
		return fail(stderr, err)
	}
	printProvider(stdout, *out)
	return 0
}

func runProviderRoute(args []string, socket string, stdout, stderr io.Writer) int {
	const usage = "usage: pluto provider route add BOX SERVICE --approve --confirm-auth | pluto provider route remove ROUTE_ID"
	if len(args) == 0 {
		return usageError(stderr, "provider route action is required", usage)
	}
	client := client.New(socket)
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("provider route add", flag.ContinueOnError)
		approved := fs.Bool("approve", false, "approve public exposure as the host owner")
		confirmed := fs.Bool("confirm-auth", false, "confirm the service keeps its own authentication enabled")
		if code := parseCommand(fs, splitFlags(args[1:], "--approve", "--confirm-auth"), stderr, usage); code != 0 {
			return code
		}
		if fs.NArg() != 2 {
			return usageError(stderr, "box id and declared service name are required", usage)
		}
		boxID, service := fs.Arg(0), fs.Arg(1)
		if !*approved || !*confirmed {
			fmt.Fprintln(stderr, "pluto: public service exposure requires --approve and --confirm-auth")
			return 1
		}
		out, err := client.AddProviderRoute("opentunnel", api.ProviderRouteRequest{BoxID: boxID, Service: service, Approved: *approved, ServiceAuthConfirmed: *confirmed})
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "OpenTunnel route %s exposes box %s service %s on port %d\n", out.Route.ID, out.Route.BoxID, out.Route.Service, out.Route.Port)
		return 0
	case "remove":
		if len(args) != 2 {
			return usageError(stderr, "route id is required", usage)
		}
		if err := client.RemoveProviderRoute("opentunnel", args[1]); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "OpenTunnel route %s removed\n", args[1])
		return 0
	default:
		return unknownSubcommand(stderr, "provider route", args[0], []string{"add", "remove"})
	}
}

func printProvider(w io.Writer, p api.ProviderStatus) {
	fmt.Fprintf(w, "%s: installed=%t enabled=%t\n", p.Info.ID, p.Status.Installed, p.Status.Enabled)
	if p.Status.Detail != "" {
		fmt.Fprintf(w, "  status: %s\n", p.Status.Detail)
	}
	printProviderDisclosure(w, p.Info)
}

func printProviderDisclosure(w io.Writer, info provider.Info) {
	// Keep the lifecycle output limited to public metadata. Provider-specific
	// status and raw subprocess output never appear here.
	fmt.Fprintf(w, "  capabilities: %s\n", joinCapabilities(info.Capabilities))
	for _, dependency := range info.Dependencies {
		fmt.Fprintf(w, "  depends on: %s\n", dependency)
	}
}

func joinCapabilities(capabilities []provider.Capability) string {
	parts := make([]string, len(capabilities))
	for i, capability := range capabilities {
		parts[i] = string(capability)
	}
	return strings.Join(parts, ", ")
}
