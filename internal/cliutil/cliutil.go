// Package cliutil is the platform-free part of the pluto command line shared
// by the Linux CLI and the Windows client. It holds usage-error printing,
// next-step failure printing with hinted errors, flag parsing in the CLI's
// voice, help-flag matching and closest-name suggestions, and replaying global
// flags onto a remote command.
//
// It imports only the standard library: the Windows binary reaches these
// helpers through internal/remotecli, which must not link the Linux-only host
// packages.
package cliutil

import (
	"flag"
	"fmt"
	"io"
)

// HintError is an error with CLI-side next steps attached, so resolution
// helpers classify a failure and a package's fail wrapper names the fix
// centrally.
type HintError struct {
	Err  error
	Next []string
}

func (e *HintError) Error() string { return e.Err.Error() }
func (e *HintError) Unwrap() error { return e.Err }

// FailText prints the `pluto:` line and its next steps, returning the failure
// exit code. The caller decides whether to add a fallback step.
func FailText(stderr io.Writer, err error, next ...string) int {
	fmt.Fprintf(stderr, "pluto: %v\n", err)
	for _, step := range next {
		fmt.Fprintf(stderr, "next: %s\n", step)
	}
	return 1
}

// ParseCommand parses a command's flags in the CLI's voice (ADR 0009, ADR
// 0011): a parse error is a usage error, printed as 'pluto: <what>' followed
// by the command's usage lines, and returns 2. Success returns 0.
func ParseCommand(fs *flag.FlagSet, args []string, stderr io.Writer, usageLines ...string) int {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		for _, line := range usageLines {
			fmt.Fprintln(stderr, line)
		}
		return 2
	}
	return 0
}

// HelpFunc returns one command's details for a name, or false when the surface
// does not document it. Each CLI keeps its own command table.
type HelpFunc func(name string) (string, bool)

// WantsHelp reports whether args ask for a command's help: -h or --help before
// any '--', after which the words belong to the work being run.
func WantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// MaybeHelp prints a command's help when args ask for it and reports whether
// it handled the invocation. A name with no doc is left to the command.
func MaybeHelp(args []string, name string, help HelpFunc, stdout io.Writer) bool {
	if !WantsHelp(args) {
		return false
	}
	text, ok := help(name)
	if !ok {
		return false
	}
	fmt.Fprint(stdout, text)
	return true
}

// MaybeHelpAtStart prints name's help only when the help flag is the first
// argument. Parent commands with subcommands use it so `device add -h` flows
// to the subcommand and prints its own details; only `device -h` is the
// parent's to answer.
func MaybeHelpAtStart(args []string, name string, help HelpFunc, stdout io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	return MaybeHelp(args[:1], name, help, stdout)
}

// Closest returns the name nearest to target, or "" when nothing is within
// two edits.
func Closest(target string, names []string) string {
	best, bestDist := "", 3
	for _, name := range names {
		if name == target {
			continue
		}
		if d := editDistance(target, name); d < bestDist {
			best, bestDist = name, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

// RemoteCommand is the command replayed on a device: the global flags that
// were set explicitly (minus --device, which selects the machine) followed by
// the command and its arguments exactly as given, `--` separators included.
func RemoteCommand(global *flag.FlagSet) []string {
	var out []string
	global.Visit(func(f *flag.Flag) {
		if f.Name == "device" {
			return
		}
		out = append(out, "--"+f.Name, f.Value.String())
	})
	return append(out, global.Args()...)
}
