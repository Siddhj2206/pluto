package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/provider"
)

const providerUsage = "usage: pluto provider list | status [ID] | install ID [--approve] | enable ID [--approve] | disable ID | remove ID | route add BOX SERVICE [--provider ID] --approve --confirm-auth | route remove ROUTE_ID [--provider ID]"

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
	const usage = "usage: pluto provider route add BOX SERVICE [--provider ID] --approve --confirm-auth | pluto provider route remove ROUTE_ID [--provider ID]"
	if len(args) == 0 {
		return usageError(stderr, "provider route action is required", usage)
	}
	client := client.New(socket)
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("provider route add", flag.ContinueOnError)
		providerID := fs.String("provider", "", "ingress-capable provider (default: the only one installed)")
		approved := fs.Bool("approve", false, "approve public exposure as the host owner")
		confirmed := fs.Bool("confirm-auth", false, "confirm the service keeps its own authentication enabled")
		if code := parseCommand(fs, splitFlags(args[1:], "--provider", "--approve", "--confirm-auth"), stderr, usage); code != 0 {
			return code
		}
		if fs.NArg() != 2 {
			return usageError(stderr, "box id and declared service name are required", usage)
		}
		boxID, service := fs.Arg(0), fs.Arg(1)
		if !*approved || !*confirmed {
			return failText(stderr, errors.New("public service exposure requires --approve and --confirm-auth"),
				fmt.Sprintf("rerun 'pluto provider route add %s %s --approve --confirm-auth'", boxID, service))
		}
		selected, err := selectIngressProvider(client, *providerID)
		if err != nil {
			return fail(stderr, err, "list providers with 'pluto provider list'")
		}
		out, err := client.AddProviderRoute(selected.Info.ID, api.ProviderRouteRequest{BoxID: boxID, Service: service, Approved: *approved, ServiceAuthConfirmed: *confirmed})
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s route %s created for box %s service %s\n", selected.Info.Name, out.Route.ID, out.Route.BoxID, out.Route.Service)
		fmt.Fprintf(stdout, "  run '%s route list' to view the assigned public hostname\n", selected.Info.ID)
		return 0
	case "remove":
		fs := flag.NewFlagSet("provider route remove", flag.ContinueOnError)
		providerID := fs.String("provider", "", "ingress-capable provider (default: the only one installed)")
		if code := parseCommand(fs, splitFlags(args[1:], "--provider"), stderr, usage); code != 0 {
			return code
		}
		if fs.NArg() != 1 {
			return usageError(stderr, "route id is required", usage)
		}
		selected, err := selectIngressProvider(client, *providerID)
		if err != nil {
			return fail(stderr, err, "list providers with 'pluto provider list'")
		}
		if err := client.RemoveProviderRoute(selected.Info.ID, fs.Arg(0)); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s route %s removed\n", selected.Info.Name, fs.Arg(0))
		return 0
	default:
		return unknownSubcommand(stderr, "provider route", args[0], []string{"add", "remove"})
	}
}

// selectIngressProvider resolves the provider that will carry a public service
// route. An explicit id must be ingress-capable; otherwise the only
// ingress-capable provider is chosen, and an ambiguous set names the choices.
// A provider may implement several capabilities, so selection is by capability
// rather than by a hardcoded provider id (#112).
func selectIngressProvider(c *client.Client, requested string) (*api.ProviderStatus, error) {
	providers, err := c.Providers()
	if err != nil {
		return nil, err
	}
	var ingress []api.ProviderStatus
	for _, p := range providers {
		if slices.Contains(p.Info.Capabilities, provider.PublicServiceIngress) {
			ingress = append(ingress, p)
		}
	}
	if requested != "" {
		for i := range ingress {
			if ingress[i].Info.ID == requested {
				return &ingress[i], nil
			}
		}
		return nil, fmt.Errorf("provider %q does not support public service ingress", requested)
	}
	switch len(ingress) {
	case 0:
		return nil, errors.New("no provider supports public service ingress")
	case 1:
		return &ingress[0], nil
	default:
		ids := make([]string, len(ingress))
		for i := range ingress {
			ids[i] = ingress[i].Info.ID
		}
		return nil, fmt.Errorf("several providers support public service ingress (%s); choose one with --provider ID", strings.Join(ids, ", "))
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
