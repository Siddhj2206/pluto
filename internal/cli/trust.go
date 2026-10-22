package cli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

const trustUsage = "usage: pluto trust [box-id|worktree] [--yes]"

// runTrust shows the exact contract revision and event/job policy a box would
// run under, asks for confirmation, then calls the existing contract-trust
// API. It never reads or writes the contract itself.
func runTrust(args []string, input io.Reader, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "trust", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "approve without asking for confirmation")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, trustUsage); code != 0 {
		return code
	}
	if fs.NArg() > 1 {
		return usageError(stderr, "trust takes at most one box or worktree", trustUsage)
	}
	target := fs.Arg(0)
	c := workClient(socket)
	box, code := boxForTarget(socket, target, stderr)
	if code != 0 {
		return code
	}
	status, err := c.ContractTrust(box.ID)
	if err != nil {
		return fail(stderr, err, "check the box worktree has a valid .pluto.toml")
	}

	fmt.Fprintf(stdout, "box:      %s (%s/%s)\n", state.ShortID(box.ID), box.Project, box.Branch)
	fmt.Fprintf(stdout, "contract: %s revision %s\n", contract.FileName, status.Revision)
	fmt.Fprintf(stdout, "decision: %s\n", status.Decision)
	if len(status.Jobs) == 0 {
		fmt.Fprintln(stdout, "jobs:     none")
	} else {
		fmt.Fprintf(stdout, "jobs:     %s\n", strings.Join(status.Jobs, ", "))
	}
	if len(status.Events) == 0 {
		fmt.Fprintln(stdout, "events:   none")
	} else {
		fmt.Fprintln(stdout, "events:")
		for _, policy := range status.Events {
			line := fmt.Sprintf("  %s -> %s", policy.Event, policy.Job)
			if len(policy.Actions) > 0 {
				line += " (" + strings.Join(policy.Actions, ", ") + ")"
			}
			if len(policy.Labels) > 0 {
				line += " [labels " + strings.Join(policy.Labels, ", ") + "]"
			}
			if policy.TrustedLabel != "" {
				line += " [trusted label " + policy.TrustedLabel + "]"
			}
			fmt.Fprintln(stdout, line)
		}
	}

	if status.Decision == state.ContractTrustApproved {
		fmt.Fprintln(stdout, "already approved")
		return 0
	}
	if !*yes {
		fmt.Fprint(stdout, "approve this exact revision for unattended work? [y/N]: ")
		line, err := bufio.NewReader(input).ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "not approved; run 'pluto trust <box> --yes' to approve it.")
			return 0
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "not approved; run 'pluto trust <box> --yes' to approve it.")
			return 0
		}
	}
	approved, err := c.ApproveContract(box.ID, status.Revision)
	if err != nil {
		return fail(stderr, err, "the contract may have changed; run 'pluto trust' again")
	}
	fmt.Fprintf(stdout, "approved revision %s for %s\n", approved.Revision, approved.Project)
	return 0
}
