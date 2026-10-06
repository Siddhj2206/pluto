package contract_test

import (
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// Environment-layer reuse is disabled by default and enabled only by an
// explicit [provision].cache opt-in.
func TestProvisionEnvironmentCacheIsOptIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{name: "disabled by default", body: "[provision]\ncommand = 'make setup'\n"},
		{name: "enabled explicitly", body: "[provision]\ncommand = 'make setup'\ncache = true\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := contract.Parse(tc.body)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.Provision.Cache != tc.want {
				t.Fatalf("provision.cache = %v, want %v", got.Provision.Cache, tc.want)
			}
		})
	}
}

// A cached provision cannot declare environment values: they would be captured
// in the reusable layer and handed to an unrelated box.
func TestProvisionEnvironmentCacheRejectsDeclaredEnvironment(t *testing.T) {
	for _, body := range []string{
		"[env]\nTOKEN = 'value'\n[provision]\ncommand = 'make setup'\ncache = true\n",
		"[provision]\ncommand = 'make setup'\ncache = true\nenv = { TOKEN = 'value' }\n",
	} {
		if _, err := contract.Parse(body); err == nil {
			t.Fatalf("Parse(%q) succeeded, want cached provision with declared env rejected", body)
		}
	}
}

func TestProvisionShareUntrustedRequiresCache(t *testing.T) {
	if _, err := contract.Parse("[provision]\ncommand = 'make setup'\nshare_untrusted = true\n"); err == nil {
		t.Fatal("share_untrusted without cache should be rejected")
	}
}

// The opt-in is a provision-only concept; declaring it on wake is a mistake.
func TestEnvironmentCacheIsProvisionOnly(t *testing.T) {
	if _, err := contract.Parse("[wake]\ncommand = 'true'\ncache = true\n"); err == nil {
		t.Fatal("wake.cache should be rejected")
	}
}

// SetupHash captures only the declared inputs that shape the provisioned disk:
// the base image, [tools], and [provision]. Work-item events, schedules, wake,
// services, jobs, and sessions do not invalidate a reusable layer.
func TestSetupHashIgnoresUnrelatedContractEdits(t *testing.T) {
	base := `
[provision]
command = "make setup"
cache = true

[wake]
command = "make wake"
`
	edited := `
[provision]
command = "make setup"
cache = true

[wake]
command = "make other-wake"

[[schedule]]
name = "nightly"
cron = "0 2 * * *"

[jobs.test]
command = "make test"

[services.web]
command = "make serve"
`
	a, err := contract.Parse(base)
	if err != nil {
		t.Fatalf("Parse(base): %v", err)
	}
	b, err := contract.Parse(edited)
	if err != nil {
		t.Fatalf("Parse(edited): %v", err)
	}
	if a.SetupHash() != b.SetupHash() {
		t.Fatalf("SetupHash changed on unrelated edits:\n  %s\n  %s", a.SetupHash(), b.SetupHash())
	}
}

func TestSetupHashChangesWhenDeclaredSetupChanges(t *testing.T) {
	base, err := contract.Parse("[box]\nimage = \"ubuntu-24.04\"\n[provision]\ncommand = \"make setup\"\ncache = true\n")
	if err != nil {
		t.Fatalf("Parse(base): %v", err)
	}
	for name, body := range map[string]string{
		"provision command": "[box]\nimage = \"ubuntu-24.04\"\n[provision]\ncommand = \"make setup --deep\"\ncache = true\n",
		"tools":             "[tools]\npackages = [\"ripgrep\"]\n[provision]\ncommand = \"make setup\"\ncache = true\n",
		"image":             "[box]\nimage = \"ubuntu-25.04\"\n[provision]\ncommand = \"make setup\"\ncache = true\n",
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := contract.Parse(body)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if base.SetupHash() == changed.SetupHash() {
				t.Fatal("SetupHash survived a declared-setup change")
			}
		})
	}
}

func TestSetupHashIgnoresFormatting(t *testing.T) {
	verbose, err := contract.Parse(`
# setup
[tools]
packages = ["ripgrep"]   # search

[provision]
cache = true
command = "make setup"
`)
	if err != nil {
		t.Fatalf("Parse(verbose): %v", err)
	}
	tight, err := contract.Parse(`[provision]
command="make setup"
cache=true

[tools]
packages=["ripgrep"]`)
	if err != nil {
		t.Fatalf("Parse(tight): %v", err)
	}
	if verbose.SetupHash() != tight.SetupHash() {
		t.Fatalf("SetupHash changed across formatting:\n  %s\n  %s", verbose.SetupHash(), tight.SetupHash())
	}
}

func TestSetupHashIsStable(t *testing.T) {
	c, err := contract.Parse("[provision]\ncommand = \"make\"\ncache = true\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.SetupHash() == "" || c.SetupHash() != c.SetupHash() {
		t.Fatalf("SetupHash = %q, want a stable non-empty value", c.SetupHash())
	}
}
