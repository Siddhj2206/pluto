package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/state"
)

const webhookUsage = "usage: pluto webhook <add|ls|rm|test> [args]"

// runWebhook manages inbound event sources through the daemon. The secret is
// named by an environment variable; the CLI never sees or prints its value.
func runWebhook(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelpAtStart(args, "webhook", commandHelp, stdout) {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, webhookUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return runWebhookAdd(args[1:], socket, stdout, stderr)
	case "ls":
		return runWebhookList(args[1:], socket, stdout, stderr)
	case "rm":
		return runWebhookRemove(args[1:], socket, stdout, stderr)
	case "test":
		return runWebhookTest(args[1:], socket, stdout, stderr)
	default:
		return unknownSubcommand(stderr, "webhook", args[0], []string{"add", "ls", "rm", "test"})
	}
}

const webhookAddUsage = "usage: pluto webhook add <github|generic> --source NAME --box BOX --secret-env ENV"

func runWebhookAdd(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "webhook add", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("webhook add", flag.ContinueOnError)
	source := fs.String("source", "", "source name used in the endpoint path")
	boxArg := fs.String("box", "", "registered box id, prefix, or worktree")
	secretEnv := fs.String("secret-env", "", "environment variable in the daemon that holds the secret")
	if code := cliutil.ParseCommand(fs, splitFlags(args, "--source", "--box", "--secret-env"), stderr, webhookAddUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "webhook add takes one kind: github or generic", webhookAddUsage)
	}
	kind := fs.Arg(0)
	if !state.ValidWebhookKind(kind) {
		return usageError(stderr, fmt.Sprintf("unsupported webhook kind %q (choose github or generic)", kind), webhookAddUsage)
	}
	if *source == "" || *boxArg == "" || *secretEnv == "" {
		return usageError(stderr, "--source, --box, and --secret-env are required", webhookAddUsage)
	}
	c := workClient(socket)
	box, err := resolveBox(c, *boxArg)
	if err != nil {
		return fail(stderr, err, "pass a box id from 'pluto ls'")
	}
	status, err := c.AddWebhook(api.WebhookAddRequest{Kind: kind, Source: *source, BoxID: box.ID, SecretEnv: *secretEnv})
	if err != nil {
		return fail(stderr, err, "export the secret in the daemon environment, then retry")
	}
	fmt.Fprintf(stdout, "registered %s source %s at %s for box %s (secret from %s)\n",
		status.Kind, status.Source, status.Endpoint, short(status.BoxID), status.SecretEnv)
	return 0
}

const webhookLsUsage = "usage: pluto webhook ls [--json]"

func runWebhookList(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "webhook ls", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("webhook ls", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := cliutil.ParseCommand(fs, args, stderr, webhookLsUsage); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "webhook ls takes no arguments", webhookLsUsage)
	}
	list, err := client.New(socket).Webhooks()
	if err != nil {
		return fail(stderr, err)
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, api.WebhookListResponse{SchemaVersion: 1, Webhooks: list})
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no webhook sources")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tKIND\tENDPOINT\tSECRET\tBOX\tLAST DELIVERY\tACCEPTED\tREJECTED\tLAST REJECTION")
	for _, item := range list {
		secret := "missing"
		if item.SecretPresent {
			secret = "present"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
			item.Source, item.Kind, item.Endpoint, secret, short(item.BoxID),
			webhookLastDelivery(item), item.Accepted, item.Rejected, item.LastRejectionReason)
	}
	tw.Flush()
	return 0
}

func webhookLastDelivery(item api.WebhookSourceStatus) string {
	if item.LastDeliveryAt == nil {
		return "-"
	}
	when := item.LastDeliveryAt.Local().Format("2006-01-02 15:04:05")
	if item.LastOutcome != "" {
		when += " " + item.LastOutcome
	}
	return when
}

const webhookRmUsage = "usage: pluto webhook rm <source>"

func runWebhookRemove(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "webhook rm", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("webhook rm", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, webhookRmUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "webhook rm takes one source", webhookRmUsage)
	}
	source := fs.Arg(0)
	if err := client.New(socket).RemoveWebhook(source); err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return fail(stderr, err, "list sources with 'pluto webhook ls'")
		}
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "removed source %s\n", source)
	return 0
}

const webhookTestUsage = "usage: pluto webhook test <source>"

func runWebhookTest(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "webhook test", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("webhook test", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, webhookTestUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "webhook test takes one source", webhookTestUsage)
	}
	source := fs.Arg(0)
	result, err := client.New(socket).TestWebhook(source)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return fail(stderr, err, "list sources with 'pluto webhook ls'")
		}
		return fail(stderr, err, "check the source is registered and its secret environment variable is set")
	}
	line := fmt.Sprintf("sent signed test delivery %s to %s: ", result.DeliveryID, result.Endpoint)
	if result.Verified {
		line += "verified"
	} else {
		line += "not verified"
	}
	if result.Outcome != "" {
		line += " (" + result.Outcome
		if result.Reason != "" {
			line += ": " + result.Reason
		}
		line += ")"
	}
	fmt.Fprintln(stdout, line)
	return 0
}
