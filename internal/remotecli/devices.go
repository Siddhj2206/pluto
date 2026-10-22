package remotecli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/console"
	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/handshake"
	"github.com/Siddhj2206/pluto/internal/shquote"
)

// RunDevice manages the client-side registry of saved ssh destinations. It
// needs no daemon: the registry and the disposable cache are files under the
// user's config and state directories.
func RunDevice(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelpAtStart(args, "device", commandHelp, stdout) {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pluto device <add|ls|rm|doctor> ...")
		return 2
	}
	switch args[0] {
	case "add":
		return runDeviceAdd(args[1:], stdout, stderr)
	case "ls":
		return runDeviceLs(args[1:], stdout, stderr)
	case "rm":
		return runDeviceRm(args[1:], stdout, stderr)
	case "doctor":
		return runDeviceDoctor(args[1:], stdout, stderr)
	default:
		return unknownSubcommand(stderr, "device", args[0], []string{"add", "ls", "rm", "doctor"})
	}
}

const deviceAddUsage = "usage: pluto device add <nickname> [destination] [--destination DEST] [--port N] [--identity FILE] [--proxy-jump JUMP] [--remote-path auto|/abs/path] [--auth auto|key|interactive] [--trust strict|accept-new] [--dry-run] [--no-probe] [--json]"

// runDeviceAdd saves a profile and runs the ordered preflight. It never
// installs or updates the host binary and never stores a password.
func runDeviceAdd(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "device add", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device add", flag.ContinueOnError)
	destination := fs.String("destination", "", "ssh alias or raw destination")
	user := fs.String("user", "", "user to prepend to the destination")
	port := fs.Int("port", 0, "ssh port override")
	identity := fs.String("identity", "", "identity file override")
	proxy := fs.String("proxy-jump", "", "ProxyJump override")
	remotePath := fs.String("remote-path", devices.RemotePathAuto, "auto or an absolute host path")
	platform := fs.String("platform", devices.PlatformAuto, "auto or linux")
	auth := fs.String("auth", devices.AuthAuto, "auto, key, or interactive")
	trust := fs.String("trust", devices.TrustAcceptNew, "strict or accept-new")
	controlMaster := fs.Bool("control-master", false, "enable ssh ControlMaster (POSIX only)")
	dryRun := fs.Bool("dry-run", false, "print the resolved route and checks, write nothing")
	noProbe := fs.Bool("no-probe", false, "save without contacting the host")
	jsonOut := fs.Bool("json", false, "print a machine-readable result")
	args = reorderFlags(args,
		map[string]bool{
			"destination": true, "user": true, "port": true, "identity": true,
			"proxy-jump": true, "remote-path": true, "platform": true,
			"auth": true, "trust": true,
		},
		map[string]bool{
			"dry-run": true, "no-probe": true, "json": true, "control-master": true,
		})
	if code := cliutil.ParseCommand(fs, args, stderr, deviceAddUsage); code != 0 {
		return code
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fmt.Fprintln(stderr, deviceAddUsage)
		return 2
	}
	nickname := fs.Arg(0)
	dest := *destination
	if fs.NArg() == 2 {
		if fs.Arg(1) == "" {
			fmt.Fprintln(stderr, "pluto: invalid destination: empty")
			fmt.Fprintln(stderr, deviceAddUsage)
			return 2
		}
		if dest != "" && dest != fs.Arg(1) {
			fmt.Fprintln(stderr, "pluto: destination given twice")
			fmt.Fprintln(stderr, deviceAddUsage)
			return 2
		}
		dest = fs.Arg(1)
	}
	if dest == "" {
		dest = nickname
	}
	if *user != "" {
		dest = *user + "@" + hostOnly(dest)
	}
	p := devices.Profile{
		Nickname:      nickname,
		Destination:   dest,
		Port:          *port,
		IdentityFile:  *identity,
		ProxyJump:     *proxy,
		RemotePath:    *remotePath,
		Platform:      *platform,
		Auth:          *auth,
		Trust:         *trust,
		ControlMaster: *controlMaster,
	}

	reg, err := openDevices()
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	if *dryRun {
		return runDeviceAddDryRun(p, *jsonOut, stdout, stderr)
	}
	if err := reg.AddProfile(p); err != nil {
		if errors.Is(err, devices.ErrInvalid) {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			fmt.Fprintln(stderr, deviceAddUsage)
			return 2
		}
		return fail(stderr, err, deviceFixHint())
	}
	if !*jsonOut {
		fmt.Fprintf(stdout, "saved device %s (%s)\n", nickname, dest)
	}

	cached, cacheDir, err := loadDeviceCache(nickname)
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	// A route change invalidates the previous host's learned path.
	if cached.Resolved.Destination != "" && cached.Resolved.Destination != dest {
		cached = devices.Cache{Nickname: nickname}
	}
	if *noProbe {
		cached.Status = devices.StatusUnverified
		cached.CheckedAt = time.Now().UTC()
		cached.Nickname = nickname
		_ = cached.Save(cacheDir)
		report := devices.Report{
			SchemaVersion: 1, Nickname: nickname, Destination: dest, Status: devices.StatusUnverified,
			Hops: []devices.Hop{{Name: "probe", Status: devices.HopSkipped, Detail: "--no-probe"}},
		}
		if *jsonOut {
			writeJSON(stdout, report)
		} else {
			printConnectionCard(stdout, p, report)
		}
		return 0
	}

	ctx := context.Background()
	ssh, _ := devices.FindSSH()
	if ssh == "" {
		report, updated := devices.Finalize(devices.Report{SchemaVersion: 1, Nickname: nickname, Destination: dest},
			[]devices.Hop{sshMissingHop()}, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 1)
	}

	report := devices.Report{SchemaVersion: 1, Nickname: nickname, Destination: dest}
	hops := []devices.Hop{{Name: "local.ssh", Status: devices.HopOK, Detail: ssh}}
	resolved, err := devices.Resolve(ctx, ssh, p)
	if err != nil {
		cached.Resolved = devices.Resolved{Destination: dest}
		hops = append(hops, devices.Hop{Name: "config.resolve", Status: devices.HopFailed, Code: devices.CodeSSHConfig,
			Detail: err.Error(), Fix: []string{fmt.Sprintf("run 'ssh -G -- %s' and fix the matching Host block", dest)}})
		report, updated := devices.Finalize(report, hops, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 1)
	}
	cached.Resolved = resolved
	hops = append(hops, devices.Hop{Name: "config.resolve", Status: devices.HopOK, Detail: dest + " -> " + resolved.Display()})

	// Host key and auth. accept-new is the profile's explicit first-use
	// acceptance; strict never silently accepts a key.
	allowPrompt := deviceIsTerminal() && p.Auth != devices.AuthKey
	hostkey, authHop := devices.ProbeConnection(ctx, ssh, p, allowPrompt)
	hops = append(hops, hostkey, authHop)
	if authHop.Status == devices.HopFailed && authHop.Code != devices.CodeHostKeyUnknown && authHop.Code != devices.CodeHostKeyChanged {
		if allowPrompt && (authHop.Code == devices.CodeAuthInteractive || authHop.Code == devices.CodeAuth) {
			fmt.Fprintf(stderr, "installing an ssh key on %s (enter the account password once)\n", dest)
			if _, keyErr := devices.PublicKey(); keyErr != nil {
				fmt.Fprintf(stderr, "warning: %v\n", keyErr)
			}
			if copyErr := devices.CopyID(ctx, ssh, p, os.Stdin, stdout, stderr); copyErr != nil {
				if errors.Is(copyErr, devices.ErrCopyIDUnavailable) {
					fmt.Fprintln(stderr, "warning: ssh-copy-id is not installed")
					if runtime.GOOS == "windows" {
						fmt.Fprintf(stderr, "next: on Windows PowerShell, run: %s\n", devices.PowerShellKeyInstall(p))
					} else {
						printKeyInstallHint(stderr, p)
					}
				} else {
					fmt.Fprintf(stderr, "warning: %v\n", copyErr)
				}
			} else {
				hostkey, authHop = devices.ProbeConnection(ctx, ssh, p, false)
				hops[len(hops)-2] = hostkey
				hops[len(hops)-1] = authHop
			}
		} else {
			printKeyInstallHint(stderr, p)
		}
	}
	if hostkey.Status == devices.HopFailed || authHop.Status == devices.HopFailed {
		report, updated := devices.Finalize(report, hops, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 0)
	}

	remote, err := devices.ProbeSystem(ctx, ssh, p)
	if err != nil {
		code := devices.CodeShell
		if errors.Is(err, devices.ErrPlatform) {
			code = devices.CodePlatform
		}
		hops = append(hops, devices.Hop{Name: "shell", Status: devices.HopFailed, Code: code, Detail: err.Error(),
			Fix: []string{"point the device at a Linux host, or at WSL2's sshd from Windows"}})
		report, updated := devices.Finalize(report, hops, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 0)
	}
	cached.Remote.OS = remote.OS
	cached.Remote.Arch = remote.Arch
	cached.Remote.Shell = remote.Shell
	hops = append(hops, devices.Hop{Name: "shell", Status: devices.HopOK, Detail: fmt.Sprintf("%s/%s, shell %s", remote.OS, remote.Arch, remote.Shell)})

	path, source, err := devices.DiscoverBinary(ctx, ssh, p, cached.Remote.Path)
	if err != nil {
		hops = append(hops, devices.Hop{Name: "pluto.lookup", Status: devices.HopFailed, Code: devices.CodePlutoMissing,
			Detail: err.Error(), Fix: plutoMissingFix(p)})
		report, updated := devices.Finalize(report, hops, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 0)
	}
	cached.Remote.Path = path
	cached.Remote.PathSource = source
	hops = append(hops, devices.Hop{Name: "pluto.lookup", Status: devices.HopOK, Detail: path + " (" + source + ")"})

	info, legacy, err := devices.Handshake(ctx, ssh, p, path)
	if err != nil {
		hops = append(hops, devices.Hop{Name: "pluto.version", Status: devices.HopFailed, Code: devices.CodePlutoVersion,
			Detail: err.Error(), Fix: []string{
				fmt.Sprintf("install the matching release on the host: 'ssh %s go install github.com/Siddhj2206/pluto/cmd/pluto@latest'", dest),
				fmt.Sprintf("then verify: 'ssh %s %s version --json'", dest, path),
			}})
		report, updated := devices.Finalize(report, hops, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 0)
	}
	cached.Remote.Version = info.Version
	cached.Remote.Protocol = info.Protocol
	cached.Remote.Capabilities = info.Capabilities
	cached.Remote.Legacy = legacy
	if legacy {
		hops = append(hops, devices.Hop{Name: "pluto.version", Status: devices.HopWarn, Code: devices.CodeLegacyVersion,
			Detail: info.Version + " reports no protocol; legacy text handshake",
			Fix:    []string{"install a newer Pluto release on the host"}})
	} else {
		hops = append(hops, devices.Hop{Name: "pluto.version", Status: devices.HopOK, Detail: fmt.Sprintf("%s (protocol %d)", info.Version, info.Protocol)})
	}
	if legacy {
		hops = append(hops, devices.Hop{Name: "protocol", Status: devices.HopWarn, Code: devices.CodeLegacyVersion, Detail: "legacy binary; capabilities unknown"})
	} else if info.Protocol != handshake.Protocol {
		hops = append(hops, devices.Hop{Name: "protocol", Status: devices.HopFailed, Code: devices.CodePlutoVersion,
			Detail: fmt.Sprintf("host protocol %d, client protocol %d", info.Protocol, handshake.Protocol),
			Fix: []string{
				fmt.Sprintf("install the matching release on the host: 'ssh %s go install github.com/Siddhj2206/pluto/cmd/pluto@latest'", dest),
				"update the client to the same release",
			}})
		report, updated := devices.Finalize(report, hops, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 0)
	} else {
		hops = append(hops, devices.Hop{Name: "protocol", Status: devices.HopOK, Detail: fmt.Sprintf("protocol %d", info.Protocol)})
	}

	reachable, daemonErr := devices.DaemonReachable(ctx, ssh, p, path)
	cached.Daemon = devices.Daemon{Reachable: reachable}
	if daemonErr != nil {
		cached.Daemon.Error = daemonErr.Error()
		hops = append(hops,
			devices.Hop{Name: "daemon", Status: devices.HopFailed, Code: devices.CodeDaemon, Detail: daemonErr.Error(),
				Fix: []string{fmt.Sprintf("start it with 'ssh %s pluto daemon'", dest), fmt.Sprintf("or install the service with 'ssh %s pluto install'", dest)}},
			devices.Hop{Name: "state", Status: devices.HopSkipped, Detail: "daemon unreachable"},
			devices.Hop{Name: "image", Status: devices.HopSkipped, Detail: "daemon unreachable"},
		)
		report, updated := devices.Finalize(report, hops, cached)
		_ = updated.Save(cacheDir)
		return printAddResult(report, p, *jsonOut, stdout, stderr, 0)
	}
	hops = append(hops, devices.Hop{Name: "daemon", Status: devices.HopOK, Detail: "reachable"},
		devices.Hop{Name: "state", Status: devices.HopOK, Detail: "readable by the daemon"})
	available, imageErr := devices.ImageAvailable(ctx, ssh, p, path)
	if imageErr != nil || !available {
		detail := "no base image imported"
		if imageErr != nil {
			detail = imageErr.Error()
		}
		hops = append(hops, devices.Hop{Name: "image", Status: devices.HopFailed, Code: devices.CodeImage, Detail: detail,
			Fix: []string{fmt.Sprintf("build and import one on the host: 'ssh %s pluto image import images/out'", dest)}})
	} else {
		hops = append(hops, devices.Hop{Name: "image", Status: devices.HopOK, Detail: "a base image is imported"})
	}
	report, updated := devices.Finalize(report, hops, cached)
	_ = updated.Save(cacheDir)
	return printAddResult(report, p, *jsonOut, stdout, stderr, 0)
}

// runDeviceAddDryRun prints the resolved route and the exact argv without
// saving anything.
func runDeviceAddDryRun(p devices.Profile, jsonOut bool, stdout, stderr io.Writer) int {
	ssh, _ := devices.FindSSH()
	if ssh == "" {
		if jsonOut {
			writeJSON(stdout, map[string]any{"schema_version": 1, "nickname": p.Nickname, "error": devices.CodeSSHMissing})
		} else {
			fmt.Fprintln(stderr, "pluto: OpenSSH client not found")
			fmt.Fprintln(stderr, "next: install OpenSSH or set PLUTO_SSH")
		}
		return 1
	}
	resolved, err := devices.Resolve(context.Background(), ssh, p)
	if err != nil {
		if jsonOut {
			writeJSON(stdout, map[string]any{"schema_version": 1, "nickname": p.Nickname, "error": devices.CodeSSHConfig, "detail": err.Error()})
		} else {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			fmt.Fprintf(stderr, "next: run 'ssh -G -- %s'\n", p.Destination)
		}
		return 1
	}
	args := p.Command(devices.SSHOptions{BatchMode: true, Strict: p.Trust == devices.TrustStrict}, "pluto", []string{"version", "--json"})
	if jsonOut {
		writeJSON(stdout, map[string]any{
			"schema_version": 1,
			"nickname":       p.Nickname,
			"destination":    p.Destination,
			"resolved":       resolved,
			"ssh":            append([]string{ssh}, args...),
			"checks":         []string{"local.ssh", "config.resolve", "hostkey", "auth", "shell", "pluto.lookup", "pluto.version", "protocol", "daemon", "state", "image"},
		})
		return 0
	}
	fmt.Fprintf(stdout, "%s -> %s\n", p.Nickname, resolved.Display())
	fmt.Fprintf(stdout, "ssh: %s\n", quoteArgv(append([]string{ssh}, args...)))
	fmt.Fprintln(stdout, "checks: local.ssh config.resolve hostkey auth shell pluto.lookup pluto.version protocol daemon state image")
	return 0
}

func runDeviceLs(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "device ls", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device ls", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, "usage: pluto device ls"); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: pluto device ls")
		return 2
	}
	reg, err := openDevices()
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NICKNAME\tDESTINATION\tROUTE\tSTATUS\tVERSION\tLAST SEEN")
	for _, p := range reg.Profiles() {
		c, _, _ := loadDeviceCache(p.Nickname)
		route := p.Destination
		if c.Resolved.Host != "" {
			route = c.Resolved.Display()
		}
		status := c.Status
		if status == "" {
			status = devices.StatusUnverified
		}
		version := c.Remote.Version
		if version == "" {
			version = "-"
		}
		last := "-"
		if c.LastOK != nil {
			last = c.LastOK.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Nickname, p.Destination, route, status, version, last)
	}
	w.Flush()
	return 0
}

func runDeviceRm(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "device rm", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device rm", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, "usage: pluto device rm <nickname>"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto device rm <nickname>")
		return 2
	}
	nickname := fs.Arg(0)
	reg, err := openDevices()
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	if err := reg.Remove(nickname); err != nil {
		return fail(stderr, err, "list saved devices with 'pluto device ls'")
	}
	if dir, err := devices.CacheDir(); err == nil {
		_ = devices.RemoveCache(dir, nickname)
	}
	fmt.Fprintf(stdout, "removed device %s\n", nickname)
	return 0
}

const deviceDoctorUsage = "usage: pluto device doctor [nickname] [--json] [--print-ssh] [--offline] [--verbose]"

// runDeviceDoctor is read-only on the host: no key installs, no config writes,
// no host-key changes.
func runDeviceDoctor(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "device doctor", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device doctor", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print a machine-readable report")
	printSSH := fs.Bool("print-ssh", false, "print the exact ssh argv with paths redacted")
	offline := fs.Bool("offline", false, "use the cache and skip network hops")
	verbose := fs.Bool("verbose", false, "show every hop detail")
	args = reorderFlags(args, nil, map[string]bool{"json": true, "print-ssh": true, "offline": true, "verbose": true})
	if code := cliutil.ParseCommand(fs, args, stderr, deviceDoctorUsage); code != 0 {
		return code
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, deviceDoctorUsage)
		return 2
	}
	reg, err := openDevices()
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	var p devices.Profile
	if fs.NArg() == 1 {
		nickname := fs.Arg(0)
		var ok bool
		p, ok = reg.Profile(nickname)
		if !ok {
			return fail(stderr, fmt.Errorf("unknown device %q", nickname), "list saved devices with 'pluto device ls'")
		}
	} else {
		list := reg.Profiles()
		if len(list) != 1 {
			fmt.Fprintln(stderr, "pluto: doctor needs a nickname")
			fmt.Fprintln(stderr, deviceDoctorUsage)
			return 2
		}
		p = list[0]
	}
	cached, cacheDir, err := loadDeviceCache(p.Nickname)
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	if *printSSH {
		return printDeviceSSH(p, stdout, stderr)
	}
	report, updated := devices.Doctor(context.Background(), p, cached, devices.DoctorOptions{Offline: *offline})
	_ = updated.Save(cacheDir)
	if *jsonOut {
		writeJSON(stdout, report)
	} else {
		printDoctorReport(stdout, report, *verbose)
	}
	if !report.OK {
		return 1
	}
	return 0
}

// printDeviceSSH prints the transport argv with the identity path redacted.
func printDeviceSSH(p devices.Profile, stdout, stderr io.Writer) int {
	ssh, _ := devices.FindSSH()
	if ssh == "" {
		fmt.Fprintln(stderr, "pluto: OpenSSH client not found")
		fmt.Fprintln(stderr, "next: install OpenSSH or set PLUTO_SSH")
		return 1
	}
	args := p.SSHArgs(devices.SSHOptions{BatchMode: true, Strict: p.Trust == devices.TrustStrict})
	args = append(args, "sh", "-c", "'printf ok'")
	redacted := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		redacted = append(redacted, args[i])
		if args[i] == "-i" && i+1 < len(args) {
			i++
			redacted = append(redacted, "<identity>")
		}
	}
	fmt.Fprintln(stdout, quoteArgv(append([]string{ssh}, redacted...)))
	return 0
}

func printDoctorReport(stdout io.Writer, report devices.Report, verbose bool) {
	status := strings.ToUpper(report.Status)
	route := report.Resolved.Display()
	if route == "" {
		route = report.Destination
	}
	fmt.Fprintf(stdout, "%s  %s  %s\n", report.Nickname, status, route)
	for _, hop := range report.Hops {
		if hop.Status == devices.HopSkipped && !verbose {
			continue
		}
		code := ""
		if hop.Code != "" {
			code = hop.Code + "  "
		}
		fmt.Fprintf(stdout, "  %s %-14s %s%s\n", hopMark(hop.Status), hop.Name, code, hop.Detail)
		for _, fix := range hop.Fix {
			fmt.Fprintf(stdout, "    next: %s\n", fix)
		}
	}
	if !report.OK {
		fmt.Fprintf(stdout, "next: rerun 'pluto device doctor %s' after fixing the failed hop\n", report.Nickname)
	}
}

func printConnectionCard(stdout io.Writer, p devices.Profile, report devices.Report) {
	route := report.Resolved.Display()
	if route == "" {
		route = p.Destination
	}
	fmt.Fprintf(stdout, "%s  %s\n", p.Nickname, report.Status)
	fmt.Fprintf(stdout, "  route     %s\n", route)
	for _, hop := range report.Hops {
		if hop.Status == devices.HopSkipped {
			continue
		}
		code := ""
		if hop.Code != "" {
			code = "  " + hop.Code
		}
		fmt.Fprintf(stdout, "  %-9s %s%s\n", hop.Name, hop.Detail, code)
		for _, fix := range hop.Fix {
			fmt.Fprintf(stdout, "  next: %s\n", fix)
		}
	}
	if report.Status == devices.StatusConnected {
		fmt.Fprintf(stdout, "  next      pluto --device %s version\n", p.Nickname)
	} else {
		fmt.Fprintf(stdout, "  next      pluto device doctor %s\n", p.Nickname)
	}
}

func printAddResult(report devices.Report, p devices.Profile, jsonOut bool, stdout, stderr io.Writer, code int) int {
	if jsonOut {
		writeJSON(stdout, report)
		return code
	}
	printConnectionCard(stdout, p, report)
	for _, hop := range report.Hops {
		if hop.Status == devices.HopFailed {
			if hop.Code != "" {
				fmt.Fprintf(stderr, "pluto: %s: %s\n", hop.Code, hop.Detail)
			}
			for _, fix := range hop.Fix {
				fmt.Fprintf(stderr, "next: %s\n", fix)
			}
		}
	}
	return code
}

func hopMark(status string) string {
	switch status {
	case devices.HopOK:
		return "✓"
	case devices.HopWarn:
		return "!"
	case devices.HopFailed:
		return "✗"
	default:
		return "-"
	}
}

func sshMissingHop() devices.Hop {
	return devices.Hop{Name: "local.ssh", Status: devices.HopFailed, Code: devices.CodeSSHMissing,
		Detail: "no ssh or ssh.exe found",
		Fix: []string{
			"install OpenSSH: on Windows run 'Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0'",
			"or set PLUTO_SSH to the ssh binary and retry",
		}}
}

// deviceIsTerminal is a seam for tests: the real check is whether stdin is a
// terminal, which a test process cannot make true without a pty.
var deviceIsTerminal = func() bool { return console.IsTerminal(os.Stdin) }

func printKeyInstallHint(stderr io.Writer, p devices.Profile) {
	if deviceIsTerminal() {
		fmt.Fprintf(stderr, "next: install your key with '%s'\n", devices.CopyIDCommand(p))
		return
	}
	fmt.Fprintln(stderr, "next: no terminal is available for a password prompt")
	fmt.Fprintf(stderr, "next: from a terminal, run '%s'\n", devices.CopyIDCommand(p))
	fmt.Fprintf(stderr, "next: on Windows PowerShell, run: %s\n", devices.PowerShellKeyInstall(p))
}

func plutoMissingFix(p devices.Profile) []string {
	return []string{
		fmt.Sprintf("install it on the host: 'ssh %s go install github.com/Siddhj2206/pluto/cmd/pluto@latest'", p.Destination),
		fmt.Sprintf("or copy the release binary to ~/.local/bin/pluto on the host, then rerun 'pluto device doctor %s'", p.Nickname),
	}
}

// reorderFlags lets flags follow positionals, which the standard flag package
// does not do. valueFlags names flags that consume the next word.
func reorderFlags(args []string, valueFlags, boolFlags map[string]bool) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		name := strings.TrimLeft(arg, "-")
		if key, _, ok := strings.Cut(name, "="); ok {
			name = key
		}
		if strings.HasPrefix(arg, "-") && arg != "-" && (valueFlags[name] || boolFlags[name]) {
			flags = append(flags, arg)
			if valueFlags[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, arg)
	}
	return append(flags, positional...)
}

func hostOnly(dest string) string {
	if _, host, ok := strings.Cut(dest, "@"); ok {
		return host
	}
	return dest
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func quoteArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shquote.Quote(arg)
	}
	return strings.Join(quoted, " ")
}

func loadDeviceCache(nickname string) (devices.Cache, string, error) {
	dir, err := devices.CacheDir()
	if err != nil {
		return devices.Cache{}, "", err
	}
	c, err := devices.LoadCache(dir, nickname)
	return c, dir, err
}
