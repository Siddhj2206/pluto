package devices_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// fakeRunner records what Verify asked it to run; tests never ssh.
type fakeRunner struct {
	target string
	argv   []string
	ctx    context.Context
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, target string, argv ...string) error {
	f.ctx, f.target, f.argv = ctx, target, argv
	return f.err
}

// registryPath returns a devices.toml path under a fresh temp dir, mimicking
// the XDG layout without touching the real config directory.
func registryPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "pluto", "devices.toml")
}

func TestAddPersistsSoANewRegistryResolvesIt(t *testing.T) {
	path := registryPath(t)
	reg, err := devices.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("fresh registry lists %v, want empty", got)
	}
	if err := reg.Add("neptuno", "siddhant@neptuno"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	reloaded, err := devices.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	target, ok := reloaded.Resolve("neptuno")
	if !ok {
		t.Fatalf("reloaded registry does not resolve neptuno; list = %v", reloaded.List())
	}
	if target != "siddhant@neptuno" {
		t.Fatalf("Resolve = %q, want siddhant@neptuno", target)
	}
}

func TestListIsSortedAndRemovePersists(t *testing.T) {
	path := registryPath(t)
	reg, err := devices.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for nickname, target := range map[string]string{
		"neptuno": "siddhant@neptuno",
		"ceres":   "siddhant@ceres",
		"eros":    "dev@eros",
	} {
		if err := reg.Add(nickname, target); err != nil {
			t.Fatalf("Add(%s): %v", nickname, err)
		}
	}

	list := reg.List()
	var names []string
	for _, d := range list {
		names = append(names, d.Nickname)
	}
	if got := strings.Join(names, ","); got != "ceres,eros,neptuno" {
		t.Fatalf("List order = %q, want ceres,eros,neptuno", got)
	}
	if list[0].Target != "siddhant@ceres" {
		t.Fatalf("List target = %q, want siddhant@ceres", list[0].Target)
	}

	if err := reg.Remove("neptuno"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	reloaded, err := devices.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := reloaded.Resolve("neptuno"); ok {
		t.Fatal("neptuno still resolves after Remove")
	}
	if _, ok := reloaded.Resolve("ceres"); !ok {
		t.Fatal("ceres disappeared with neptuno")
	}
	if err := reloaded.Remove("missing"); err == nil {
		t.Fatal("removing an unknown device should fail")
	}
}

func TestAddOverwritesAnExistingNickname(t *testing.T) {
	path := registryPath(t)
	reg, err := devices.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := reg.Add("neptuno", "siddhant@neptuno"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := reg.Add("neptuno", "root@neptuno"); err != nil {
		t.Fatalf("Add overwrite: %v", err)
	}
	if target, _ := reg.Resolve("neptuno"); target != "root@neptuno" {
		t.Fatalf("Resolve = %q, want root@neptuno", target)
	}
	if got := len(reg.List()); got != 1 {
		t.Fatalf("List has %d devices, want 1", got)
	}
}

func TestAddRejectsInvalidNicknamesAndTargets(t *testing.T) {
	path := registryPath(t)
	reg, err := devices.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, tc := range []struct{ nickname, target string }{
		{"", "siddhant@neptuno"},
		{"two words", "siddhant@neptuno"},
		{"neptuno", ""},
		{"neptuno", "two words"},
		{"neptuno", "-oProxyCommand=boom"},
	} {
		if err := reg.Add(tc.nickname, tc.target); err == nil {
			t.Fatalf("Add(%q, %q) succeeded, want an error", tc.nickname, tc.target)
		}
	}
	if got := len(reg.List()); got != 0 {
		t.Fatalf("invalid adds left %d devices, want 0", got)
	}
}

func TestOpenFailsOnMalformedTOML(t *testing.T) {
	path := registryPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("this is not = = toml\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := devices.Open(path); err == nil {
		t.Fatal("Open on malformed TOML should fail")
	}
}

func TestDefaultPathFollowsXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := devices.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, "pluto", "devices.toml"); path != want {
		t.Fatalf("DefaultPath = %q, want %q", path, want)
	}
}

func TestVerifyProbesTheTargetWithABoundedWait(t *testing.T) {
	fr := &fakeRunner{}
	if err := devices.Verify(context.Background(), fr, "siddhant@neptuno"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if fr.target != "siddhant@neptuno" {
		t.Fatalf("runner target = %q, want siddhant@neptuno", fr.target)
	}
	if got := strings.Join(fr.argv, " "); got != "pluto version" {
		t.Fatalf("runner argv = %q, want %q", got, "pluto version")
	}
	if _, ok := fr.ctx.Deadline(); !ok {
		t.Fatal("Verify passed a context with no deadline; a dead host would hang it")
	}
}

func TestVerifyReportsTargetsThatDoNotAnswer(t *testing.T) {
	fr := &fakeRunner{err: errors.New("exit status 255")}
	err := devices.Verify(context.Background(), fr, "siddhant@neptuno")
	if err == nil {
		t.Fatal("Verify should report a failed probe")
	}
	if !strings.Contains(err.Error(), "siddhant@neptuno") {
		t.Fatalf("Verify error = %q, want it to name the target", err)
	}
}
