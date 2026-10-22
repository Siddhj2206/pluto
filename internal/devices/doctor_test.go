package devices_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// stubConn satisfies net.Conn for the doctor's TCP hop without a socket.
type stubConn struct{}

func (stubConn) Read([]byte) (int, error)         { return 0, errors.New("closed") }
func (stubConn) Write(b []byte) (int, error)      { return len(b), nil }
func (stubConn) Close() error                     { return nil }
func (stubConn) LocalAddr() net.Addr              { return stubAddr{} }
func (stubConn) RemoteAddr() net.Addr             { return stubAddr{} }
func (stubConn) SetDeadline(time.Time) error      { return nil }
func (stubConn) SetReadDeadline(time.Time) error  { return nil }
func (stubConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddr struct{}

func (stubAddr) Network() string { return "tcp" }
func (stubAddr) String() string  { return "127.0.0.1:22" }

func doctorOpts() devices.DoctorOptions {
	return devices.DoctorOptions{
		LookupHost: func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return stubConn{}, nil
		},
	}
}

func doctorRules() []testssh.Rule {
	return []testssh.Rule{
		{Match: "-G", Stdout: "host 127.0.0.1\nuser siddhant\nport 22\nproxyjump none\n"},
		{Match: "printf ok", Stdout: "ok"},
		{Match: "uname -s", Stdout: "os=Linux\narch=x86_64\nshell=/bin/sh\n"},
		{Match: "command -v pluto", Stdout: "/home/sid/.local/bin/pluto\n"},
		{Match: "version --json", Stdout: `{"schema_version":1,"version":"v0.1.5","protocol":1,"capabilities":["json"]}` + "\n"},
		{Match: "image ls", Stdout: "VERSION\tSNAPSHOT\n0.1.5\t2026-01-01\n"},
		{Match: "pluto ls", Stdout: "ID\tPROJECT/BRANCH\n"},
	}
}

func TestDoctorHealthyOrderedHops(t *testing.T) {
	testssh.StartScript(t, doctorRules()...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, cache := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	if !report.OK || report.Status != devices.StatusConnected {
		t.Fatalf("report = %+v, want connected", report)
	}
	wantNames := []string{"local.ssh", "config.resolve", "dns", "tcp", "proxy", "hostkey", "auth", "shell", "pluto.lookup", "pluto.version", "protocol", "daemon", "state", "image"}
	if len(report.Hops) != len(wantNames) {
		t.Fatalf("hops = %d, want %d", len(report.Hops), len(wantNames))
	}
	for i, want := range wantNames {
		if report.Hops[i].Name != want {
			t.Fatalf("hop %d = %q, want %q", i, report.Hops[i].Name, want)
		}
		if report.Hops[i].Status != devices.HopOK && report.Hops[i].Status != devices.HopSkipped && report.Hops[i].Status != devices.HopWarn {
			t.Fatalf("hop %s = %+v, want ok/warn/skipped", want, report.Hops[i])
		}
	}
	if cache.Status != devices.StatusConnected || cache.LastOK == nil {
		t.Fatalf("cache = %+v, want connected with last_ok", cache)
	}
}

func TestDoctorClassifiesAHostKeyChange(t *testing.T) {
	rules := doctorRules()
	rules[1] = testssh.Rule{Match: "printf ok", Stderr: "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\n", Exit: 255}
	testssh.StartScript(t, rules...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, _ := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	hop := findHop(report, "hostkey")
	if hop.Status != devices.HopFailed || hop.Code != devices.CodeHostKeyChanged {
		t.Fatalf("hostkey = %+v, want E_HOSTKEY_CHANGED", hop)
	}
	if !strings.Contains(strings.Join(hop.Fix, " "), "ssh-keygen -R") {
		t.Fatalf("fix = %q, want ssh-keygen -R", hop.Fix)
	}
	if report.Status != devices.StatusOffline {
		t.Fatalf("status = %q, want offline", report.Status)
	}
	if auth := findHop(report, "auth"); auth.Status != devices.HopSkipped {
		t.Fatalf("auth = %+v, want skipped after a host-key failure", auth)
	}
}

func TestDoctorClassifiesInteractiveAuth(t *testing.T) {
	rules := doctorRules()
	rules[1] = testssh.Rule{Match: "printf ok", Stderr: "siddhant@neptuno: Permission denied (keyboard-interactive).\n", Exit: 255}
	testssh.StartScript(t, rules...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, _ := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	hop := findHop(report, "auth")
	if hop.Status != devices.HopFailed || hop.Code != devices.CodeAuthInteractive {
		t.Fatalf("auth = %+v, want E_AUTH_INTERACTIVE", hop)
	}
	if !strings.Contains(strings.Join(hop.Fix, " "), "--auth interactive") {
		t.Fatalf("fix = %q, want --auth interactive", hop.Fix)
	}
}

func TestDoctorMissingSSH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PLUTO_SSH", "")
	report, _ := devices.Doctor(context.Background(), devices.Profile{Nickname: "n", Destination: "n"}, devices.Cache{}, doctorOpts())
	hop := findHop(report, "local.ssh")
	if hop.Code != devices.CodeSSHMissing {
		t.Fatalf("local.ssh = %+v, want E_SSH_MISSING", hop)
	}
	if len(report.Hops) != 14 {
		t.Fatalf("hops = %d, want all 14 reported", len(report.Hops))
	}
}

func TestDoctorOfflineUsesTheCache(t *testing.T) {
	cached := devices.Cache{
		Nickname: "n", Status: devices.StatusConnected,
		Resolved: devices.Resolved{Destination: "n", Host: "home.example.net", User: "sid", Port: 2222},
		Daemon:   devices.Daemon{Reachable: true},
	}
	testssh.Start(t, "", "", 0)
	report, _ := devices.Doctor(context.Background(), devices.Profile{Nickname: "n", Destination: "n"}, cached,
		devices.DoctorOptions{Offline: true})
	if findHop(report, "dns").Status != devices.HopSkipped || findHop(report, "tcp").Status != devices.HopSkipped {
		t.Fatalf("offline hops = %+v, want dns/tcp skipped", report.Hops)
	}
	if findHop(report, "config.resolve").Status != devices.HopOK {
		t.Fatalf("config.resolve = %+v, want the cached route", findHop(report, "config.resolve"))
	}
}

func TestDoctorNamesAMissingHostBinary(t *testing.T) {
	rules := doctorRules()
	rules[3] = testssh.Rule{Match: "command -v pluto", Exit: 127}
	rules = append(rules, testssh.Rule{Match: "for p in", Exit: 127})
	testssh.StartScript(t, rules...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, _ := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	hop := findHop(report, "pluto.lookup")
	if hop.Status != devices.HopFailed || hop.Code != devices.CodePlutoMissing {
		t.Fatalf("pluto.lookup = %+v, want E_PLUTO_MISSING", hop)
	}
	if !strings.Contains(strings.Join(hop.Fix, " "), "go install") {
		t.Fatalf("fix = %q, want the host-side install command", hop.Fix)
	}
}

func TestDoctorNamesADaemonDown(t *testing.T) {
	rules := doctorRules()
	rules[6] = testssh.Rule{Match: "pluto ls", Stderr: "cannot reach the pluto daemon\n", Exit: 1}
	testssh.StartScript(t, rules...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, _ := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	hop := findHop(report, "daemon")
	if hop.Status != devices.HopFailed || hop.Code != devices.CodeDaemon {
		t.Fatalf("daemon = %+v, want E_DAEMON", hop)
	}
	if report.Status != devices.StatusUnverified {
		t.Fatalf("status = %q, want unverified", report.Status)
	}
	if findHop(report, "image").Status != devices.HopSkipped {
		t.Fatalf("image = %+v, want skipped when the daemon is down", findHop(report, "image"))
	}
}

func TestDoctorNamesAMissingImage(t *testing.T) {
	rules := doctorRules()
	rules[5] = testssh.Rule{Match: "image ls", Stdout: "VERSION\tSNAPSHOT\n"}
	testssh.StartScript(t, rules...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, _ := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	hop := findHop(report, "image")
	if hop.Status != devices.HopFailed || hop.Code != devices.CodeImage {
		t.Fatalf("image = %+v, want E_IMAGE", hop)
	}
	if !strings.Contains(strings.Join(hop.Fix, " "), "image import") {
		t.Fatalf("fix = %q, want image import", hop.Fix)
	}
}

func TestDoctorRejectsAProtocolMismatch(t *testing.T) {
	rules := doctorRules()
	rules[4] = testssh.Rule{Match: "version --json", Stdout: `{"schema_version":1,"version":"v9","protocol":9,"capabilities":[]}` + "\n"}
	testssh.StartScript(t, rules...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, _ := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	hop := findHop(report, "protocol")
	if hop.Status != devices.HopFailed || hop.Code != devices.CodePlutoVersion {
		t.Fatalf("protocol = %+v, want E_PLUTO_VERSION", hop)
	}
}

func TestDoctorToleratesALegacyTextVersion(t *testing.T) {
	rules := doctorRules()
	rules[4] = testssh.Rule{Match: "version --json", Exit: 2}
	rules = append(rules, testssh.Rule{Match: "version", Stdout: "pluto 0.1.0\n"})
	testssh.StartScript(t, rules...)

	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	report, _ := devices.Doctor(context.Background(), p, devices.Cache{}, doctorOpts())
	hop := findHop(report, "pluto.version")
	if hop.Status != devices.HopWarn || hop.Code != devices.CodeLegacyVersion {
		t.Fatalf("pluto.version = %+v, want a legacy warning", hop)
	}
	if report.Status != devices.StatusConnected {
		t.Fatalf("status = %q, want connected with a warning", report.Status)
	}
}

func findHop(report devices.Report, name string) devices.Hop {
	for _, hop := range report.Hops {
		if hop.Name == name {
			return hop
		}
	}
	return devices.Hop{}
}
