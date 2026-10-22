package devices

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/handshake"
)

// Stable doctor codes. Each failed hop carries one, and the fix list names the
// exact next command.
const (
	CodeSSHMissing      = "E_SSH_MISSING"
	CodeSSHConfig       = "E_SSH_CONFIG"
	CodeDNS             = "E_DNS"
	CodeTCPRefused      = "E_TCP_REFUSED"
	CodeTCPTimeout      = "E_TCP_TIMEOUT"
	CodeProxy           = "E_PROXY"
	CodeHostKeyUnknown  = "E_HOSTKEY_UNKNOWN"
	CodeHostKeyChanged  = "E_HOSTKEY_CHANGED"
	CodeAuth            = "E_AUTH"
	CodeAuthInteractive = "E_AUTH_INTERACTIVE"
	CodeShell           = "E_SHELL"
	CodePlatform        = "E_PLATFORM"
	CodePlutoMissing    = "E_PLUTO_MISSING"
	CodePlutoVersion    = "E_PLUTO_VERSION"
	CodeDaemon          = "E_DAEMON"
	CodeImage           = "E_IMAGE"
	CodeOffline         = "E_OFFLINE"
	CodeLegacyVersion   = "W_LEGACY_VERSION"
)

// Hop statuses.
const (
	HopOK      = "ok"
	HopWarn    = "warn"
	HopFailed  = "failed"
	HopSkipped = "skipped"
)

// Hop is one ordered doctor check.
type Hop struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Code   string   `json:"code,omitempty"`
	Detail string   `json:"detail,omitempty"`
	Fix    []string `json:"fix,omitempty"`
}

// Report is the stable `device doctor --json` document.
type Report struct {
	SchemaVersion int      `json:"schema_version"`
	Nickname      string   `json:"nickname"`
	Destination   string   `json:"destination"`
	Status        string   `json:"status"`
	Resolved      Resolved `json:"resolved"`
	Hops          []Hop    `json:"hops"`
	OK            bool     `json:"ok"`
}

// DoctorOptions injects the environment so the network hops are testable.
type DoctorOptions struct {
	// SSH overrides the discovered ssh binary.
	SSH string
	// Offline uses the cache and marks network hops skipped.
	Offline bool
	// LookupHost defaults to net.DefaultResolver.LookupHost.
	LookupHost func(ctx context.Context, host string) ([]string, error)
	// Dial defaults to net.Dialer.DialContext.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout bounds each network hop.
	Timeout time.Duration
}

var doctorHopNames = []string{
	"local.ssh",
	"config.resolve",
	"dns",
	"tcp",
	"proxy",
	"hostkey",
	"auth",
	"shell",
	"pluto.lookup",
	"pluto.version",
	"protocol",
	"daemon",
	"state",
	"image",
}

// Doctor checks the full path in order and never changes the host: no key
// installs, no ssh config writes, no host-key edits.
func Doctor(ctx context.Context, p Profile, cached Cache, opts DoctorOptions) (Report, Cache) {
	p.applyDefaults()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	lookup := opts.LookupHost
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	dial := opts.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: timeout}
		dial = d.DialContext
	}

	now := time.Now().UTC()
	rep := Report{SchemaVersion: 1, Nickname: p.Nickname, Destination: p.Destination}
	cache := cached
	cache.Nickname = p.Nickname
	cache.CheckedAt = now

	hops := make([]Hop, 0, len(doctorHopNames))
	next := 0
	add := func(name, status, code, detail string, fix ...string) {
		hops = append(hops, Hop{Name: name, Status: status, Code: code, Detail: detail, Fix: fix})
		next++
	}
	skipRest := func() {
		for next < len(doctorHopNames) {
			add(doctorHopNames[next], HopSkipped, "", "not reached", nil...)
		}
	}

	// 1. local.ssh
	ssh := opts.SSH
	if ssh == "" {
		ssh, _ = FindSSH()
	}
	if ssh == "" {
		add("local.ssh", HopFailed, CodeSSHMissing, "no ssh or ssh.exe found",
			"install OpenSSH: on Windows run 'Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0'",
			"or set PLUTO_SSH to the ssh binary and retry")
		skipRest()
		return Finalize(rep, hops, cache)
	}
	version := sshVersion(ctx, ssh)
	add("local.ssh", HopOK, "", version)

	// 2. config.resolve
	if opts.Offline {
		if cached.Resolved.Host != "" {
			rep.Resolved = cached.Resolved
			add("config.resolve", HopOK, "", cached.Resolved.Display()+" (cached)")
		} else {
			add("config.resolve", HopSkipped, "", "offline and no cached resolution")
		}
		for _, name := range []string{"dns", "tcp", "proxy", "hostkey", "auth", "shell", "pluto.lookup", "pluto.version", "protocol"} {
			add(name, HopSkipped, "", "offline")
		}
		cachedDaemon := hopForCachedDaemon(cached)
		add("daemon", cachedDaemon.Status, cachedDaemon.Code, cachedDaemon.Detail)
		add("state", HopSkipped, "", "offline")
		add("image", HopSkipped, "", "offline")
		return Finalize(rep, hops, cache)
	}

	resolved, err := Resolve(ctx, ssh, p)
	if err != nil {
		cache.Resolved = Resolved{Destination: p.Destination}
		add("config.resolve", HopFailed, CodeSSHConfig, err.Error(),
			fmt.Sprintf("run 'ssh -G -- %s' and fix the matching Host block in ~/.ssh/config", p.Destination))
		skipRest()
		return Finalize(rep, hops, cache)
	}
	rep.Resolved = resolved
	cache.Resolved = resolved
	add("config.resolve", HopOK, "", p.Destination+" -> "+resolved.Display())

	// 3. dns
	if ip := net.ParseIP(resolved.Host); ip != nil {
		add("dns", HopOK, "", resolved.Host+" is an IP address")
	} else if _, err := lookupWithTimeout(ctx, lookup, resolved.Host, timeout); err != nil {
		add("dns", HopFailed, CodeDNS, err.Error(),
			"check the VPN, DNS, and the hostname",
			fmt.Sprintf("run 'ssh -G -- %s' to see the effective HostName", p.Destination))
		skipRest()
		return Finalize(rep, hops, cache)
	} else {
		add("dns", HopOK, "", resolved.Host+" resolves")
	}

	// 4. tcp
	addr := net.JoinHostPort(resolved.Host, strconv.Itoa(resolved.Port))
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		code, detail := classifyDial(err, addr)
		add("tcp", HopFailed, code, detail,
			fmt.Sprintf("check sshd is listening and reachable at %s", addr),
			fmt.Sprintf("run 'ssh -p %d %s'", resolved.Port, resolved.Host))
		skipRest()
		return Finalize(rep, hops, cache)
	}
	conn.Close()
	add("tcp", HopOK, "", "connected to "+addr)

	// 5. proxy
	if resolved.ProxyJump == "" {
		add("proxy", HopSkipped, "", "no ProxyJump")
	} else {
		if err := checkProxy(ctx, ssh, resolved.ProxyJump, lookup, dial, timeout); err != nil {
			add("proxy", HopFailed, CodeProxy, err.Error(),
				fmt.Sprintf("run 'ssh -J %s -v %s'", resolved.ProxyJump, p.Destination),
				"fix the jump host's route or ~/.ssh/config entry")
			skipRest()
			return Finalize(rep, hops, cache)
		}
		add("proxy", HopOK, "", "jump host "+resolved.ProxyJump+" resolves and connects")
	}

	// 6/7. hostkey and auth share one strict probe.
	probeOpts := SSHOptions{BatchMode: true, Trust: TrustStrict}
	res, runErr := runSSH(ctx, ssh, p.ScriptCommand(probeOpts, "printf ok"), 20*time.Second)
	hostKeyHop, authHop := classifyProbe(res, runErr)
	add("hostkey", hostKeyHop.Status, hostKeyHop.Code, hostKeyHop.Detail, hostKeyHop.Fix...)
	add("auth", authHop.Status, authHop.Code, authHop.Detail, authHop.Fix...)
	if hostKeyHop.Status == HopFailed || authHop.Status == HopFailed {
		skipRest()
		return Finalize(rep, hops, cache)
	}

	// 8. shell
	remote, err := ProbeSystem(ctx, ssh, p)
	if err != nil {
		code := CodeShell
		if errors.Is(err, ErrPlatform) {
			code = CodePlatform
		}
		add("shell", HopFailed, code, err.Error(), shellFix(code)...)
		skipRest()
		return Finalize(rep, hops, cache)
	}
	cache.Remote.OS = remote.OS
	cache.Remote.Arch = remote.Arch
	cache.Remote.Shell = remote.Shell
	add("shell", HopOK, "", fmt.Sprintf("%s/%s, shell %s", remote.OS, remote.Arch, remote.Shell))

	// 9. pluto.lookup
	path, source, err := DiscoverBinary(ctx, ssh, p, cached.Remote.Path)
	if err != nil {
		add("pluto.lookup", HopFailed, CodePlutoMissing, err.Error(), plutoMissingFix(p, cached)...)
		skipRest()
		return Finalize(rep, hops, cache)
	}
	cache.Remote.Path = path
	cache.Remote.PathSource = source
	add("pluto.lookup", HopOK, "", path+" ("+source+")")

	// 10. pluto.version
	info, legacy, err := Handshake(ctx, ssh, p, path)
	if err != nil {
		add("pluto.version", HopFailed, CodePlutoVersion, err.Error(),
			fmt.Sprintf("install the matching release on the host: 'ssh %s go install github.com/Siddhj2206/pluto/cmd/pluto@latest'", p.Destination),
			fmt.Sprintf("then verify: 'ssh %s %s version --json'", p.Destination, path))
		skipRest()
		return Finalize(rep, hops, cache)
	}
	cache.Remote.Version = info.Version
	cache.Remote.Protocol = info.Protocol
	cache.Remote.Capabilities = info.Capabilities
	cache.Remote.Legacy = legacy
	if legacy {
		add("pluto.version", HopWarn, CodeLegacyVersion, info.Version+" reports no protocol; legacy text handshake",
			"install a newer Pluto release on the host to get protocol and capability checks")
	} else {
		add("pluto.version", HopOK, "", info.Version+" (protocol "+strconv.Itoa(info.Protocol)+")")
	}

	// 11. protocol
	if legacy {
		add("protocol", HopWarn, CodeLegacyVersion, "legacy binary; capabilities unknown",
			"install a newer Pluto release on the host")
	} else if info.Protocol != handshake.Protocol {
		add("protocol", HopFailed, CodePlutoVersion,
			fmt.Sprintf("host protocol %d, client protocol %d", info.Protocol, handshake.Protocol),
			fmt.Sprintf("install the matching release on the host: 'ssh %s go install github.com/Siddhj2206/pluto/cmd/pluto@latest'", p.Destination),
			"update the client to the same release")
		skipRest()
		return Finalize(rep, hops, cache)
	} else {
		add("protocol", HopOK, "", fmt.Sprintf("protocol %d, %s", info.Protocol, strings.Join(info.Capabilities, ",")))
	}

	// 12. daemon
	reachable, daemonErr := DaemonReachable(ctx, ssh, p, path)
	cache.Daemon = Daemon{Reachable: reachable}
	if daemonErr != nil {
		cache.Daemon.Error = daemonErr.Error()
		add("daemon", HopFailed, CodeDaemon, daemonErr.Error(),
			fmt.Sprintf("start it with 'ssh %s pluto daemon'", p.Destination),
			fmt.Sprintf("or install the user service with 'ssh %s pluto install'", p.Destination))
		add("state", HopSkipped, "", "daemon unreachable")
		add("image", HopSkipped, "", "daemon unreachable")
		return Finalize(rep, hops, cache)
	}
	add("daemon", HopOK, "", "reachable")

	// 13. state
	add("state", HopOK, "", "state directory readable by the daemon")

	// 14. image
	available, imageErr := ImageAvailable(ctx, ssh, p, path)
	if imageErr != nil || !available {
		detail := "no base image imported"
		if imageErr != nil {
			detail = imageErr.Error()
		}
		add("image", HopFailed, CodeImage, detail,
			fmt.Sprintf("build and import one on the host: 'ssh %s pluto image import images/out'", p.Destination))
		return Finalize(rep, hops, cache)
	}
	add("image", HopOK, "", "a base image is imported")
	return Finalize(rep, hops, cache)
}

// Finalize sets the report's derived status and the cache's last outcome from
// an ordered hop list. Add and doctor share it so a saved-but-unreachable
// device is never reported connected.
func Finalize(rep Report, hops []Hop, cache Cache) (Report, Cache) {
	rep.Hops = hops
	rep.OK = true
	offline := false
	for _, hop := range hops {
		if hop.Status != HopFailed {
			continue
		}
		rep.OK = false
		switch hop.Code {
		case CodeSSHMissing, CodeSSHConfig, CodeDNS, CodeTCPRefused, CodeTCPTimeout,
			CodeProxy, CodeHostKeyUnknown, CodeHostKeyChanged, CodeAuth,
			CodeAuthInteractive, CodeOffline:
			offline = true
		}
	}
	switch {
	case offline:
		rep.Status = StatusOffline
	case !rep.OK:
		rep.Status = StatusUnverified
	default:
		rep.Status = StatusConnected
	}
	cache.Status = rep.Status
	if rep.Status == StatusConnected {
		now := time.Now().UTC()
		cache.LastOK = &now
		cache.LastError = nil
	} else {
		for _, hop := range hops {
			if hop.Status == HopFailed {
				msg := hop.Code + ": " + hop.Detail
				cache.LastError = &msg
				break
			}
		}
	}
	rep.Resolved = cache.Resolved
	return rep, cache
}

func hopForCachedDaemon(c Cache) Hop {
	if c.Daemon.Reachable {
		return Hop{Name: "daemon", Status: HopOK, Detail: "reachable (cached)"}
	}
	if c.Daemon.Error != "" {
		return Hop{Name: "daemon", Status: HopFailed, Code: CodeOffline, Detail: c.Daemon.Error}
	}
	return Hop{Name: "daemon", Status: HopSkipped, Detail: "offline and no cached check"}
}

// execCommandOutput runs a command and returns both streams combined; ssh -V
// writes its version to stderr, so the caller only needs the text.
func execCommandOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// sshVersion reports OpenSSH's own version. ssh -V writes it to stderr.
func sshVersion(ctx context.Context, ssh string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := execCommandOutput(ctx, ssh, "-V")
	if err != nil && out == "" {
		return ssh
	}
	return firstNonEmptyLine(out)
}

func classifyDial(err error, addr string) (code, detail string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTCPTimeout, "timed out connecting to " + addr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return CodeTCPTimeout, "timed out connecting to " + addr
	}
	if strings.Contains(strings.ToLower(err.Error()), "connection refused") {
		return CodeTCPRefused, "connection refused at " + addr
	}
	return CodeTCPTimeout, err.Error()
}

func lookupWithTimeout(ctx context.Context, lookup func(context.Context, string) ([]string, error), host string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return lookup(ctx, host)
}

func checkProxy(ctx context.Context, ssh, jump string, lookup func(context.Context, string) ([]string, error), dial func(context.Context, string, string) (net.Conn, error), timeout time.Duration) error {
	for _, hop := range strings.Split(jump, ",") {
		hop = strings.TrimSpace(hop)
		if hop == "" {
			continue
		}
		r, err := Resolve(ctx, ssh, Profile{Nickname: "jump", Destination: hop})
		if err != nil {
			return err
		}
		if net.ParseIP(r.Host) == nil {
			if _, err := lookupWithTimeout(ctx, lookup, r.Host, timeout); err != nil {
				return err
			}
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		conn, err := dial(ctx, "tcp", net.JoinHostPort(r.Host, strconv.Itoa(r.Port)))
		cancel()
		if err != nil {
			return err
		}
		conn.Close()
	}
	return nil
}

func classifyProbe(res sshResult, runErr error) (hostkey, auth Hop) {
	if runErr != nil {
		return Hop{Status: HopWarn, Code: CodeOffline, Detail: runErr.Error()},
			Hop{Status: HopFailed, Code: CodeOffline, Detail: runErr.Error(),
				Fix: []string{"retry when the host is reachable", "run 'pluto device doctor <nickname>'"}}
	}
	if res.code == 0 {
		return Hop{Status: HopOK, Detail: "host key accepted"},
			Hop{Status: HopOK, Detail: "publickey"}
	}
	text := strings.ToLower(res.stderr)
	switch {
	case strings.Contains(text, "remote host identification has changed"):
		return Hop{Status: HopFailed, Code: CodeHostKeyChanged, Detail: lastLine(res.stderr),
				Fix: []string{"verify the new key out of band, then run 'ssh-keygen -R <host>'"}},
			Hop{Status: HopSkipped, Detail: "host key changed"}
	case strings.Contains(text, "host key verification failed"):
		return Hop{Status: HopFailed, Code: CodeHostKeyUnknown, Detail: lastLine(res.stderr),
				Fix: []string{"verify the fingerprint out of band",
					"rerun 'pluto device add <nickname> <destination> --trust accept-new'"}},
			Hop{Status: HopSkipped, Detail: "host key not trusted"}
	case strings.Contains(text, "permission denied"):
		code, fix := CodeAuth, []string{"run 'ssh-add -l' to check the agent", "run 'ssh-copy-id <destination>' to install your key", "run 'ssh -v <destination>' for detail"}
		if strings.Contains(text, "password") || strings.Contains(text, "keyboard-interactive") {
			code = CodeAuthInteractive
			fix = []string{"install a key with 'ssh-copy-id <destination>'", "or rerun 'pluto device add <nickname> <destination> --auth interactive'"}
		}
		return Hop{Status: HopOK, Detail: "host key accepted"},
			Hop{Status: HopFailed, Code: code, Detail: lastLine(res.stderr), Fix: fix}
	default:
		detail := lastLine(res.stderr)
		if detail == "" {
			detail = fmt.Sprintf("ssh exited %d", res.code)
		}
		return Hop{Status: HopWarn, Code: CodeOffline, Detail: detail},
			Hop{Status: HopFailed, Code: CodeOffline, Detail: detail,
				Fix: []string{"retry when the host is reachable", "run 'ssh -v <destination>'"}}
	}
}

func shellFix(code string) []string {
	if code == CodePlatform {
		return []string{"point the device at a Linux host, or at WSL2's sshd from Windows"}
	}
	return []string{"ensure the host has /bin/sh and a POSIX userland", "use a supported Linux host"}
}

func plutoMissingFix(p Profile, c Cache) []string {
	version := c.Remote.Version
	if version == "" {
		version = "v0.1.5"
	}
	return []string{
		fmt.Sprintf("install it on the host: 'ssh %s go install github.com/Siddhj2206/pluto/cmd/pluto@%s'", p.Destination, version),
		fmt.Sprintf("or copy the release binary to ~/.local/bin/pluto on the host, then rerun 'pluto device doctor %s'", p.Nickname),
	}
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
