package cli

import (
	"flag"
	"io"

	"github.com/Siddhj2206/pluto/internal/remotecli"
)

// runRemote re-executes the command on a device over ssh. The behavior is
// shared with the Windows client; see internal/remotecli.
func runRemote(deviceName string, argv []string, stdout, stderr io.Writer) int {
	if deviceName == "" || len(argv) == 0 {
		usage(stderr)
		return 2
	}
	return remotecli.Run(deviceName, argv, stdout, stderr)
}

// remoteCommand is the command replayed on a device: the global flags that
// were set explicitly (minus --device, which selects the machine) followed by
// the command and its arguments exactly as given, `--` separators included.
func remoteCommand(global *flag.FlagSet) []string {
	var out []string
	global.Visit(func(f *flag.Flag) {
		if f.Name == "device" {
			return
		}
		out = append(out, "--"+f.Name, f.Value.String())
	})
	return append(out, global.Args()...)
}
