package contract_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// [tools] packages is a list of apt package names, or tables with an
// optional exact version (#78, docs/research/project-packaging.md).
func TestParseToolsPackagesAsNames(t *testing.T) {
	c, err := contract.Parse(`
[tools]
packages = ["build-essential", "curl", "git"]
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Tools == nil || len(c.Tools.Packages) != 3 {
		t.Fatalf("tools = %+v, want three packages", c.Tools)
	}
	want := []string{"build-essential", "curl", "git"}
	for i, name := range want {
		if got := c.Tools.Packages[i]; got.Name != name || got.Version != "" {
			t.Fatalf("packages[%d] = %+v, want %q with no version", i, got, name)
		}
	}
}

func TestParseToolsPackagesWithVersion(t *testing.T) {
	c, err := contract.Parse(`
[tools]
packages = [
  { name = "nodejs", version = "22.11.0" },
  { name = "curl" },
]
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Tools.Packages) != 2 {
		t.Fatalf("packages = %+v, want two", c.Tools.Packages)
	}
	if got := c.Tools.Packages[0]; got.Name != "nodejs" || got.Version != "22.11.0" {
		t.Fatalf("pinned package = %+v", got)
	}
	if got := c.Tools.Packages[1]; got.Name != "curl" || got.Version != "" {
		t.Fatalf("bare package = %+v", got)
	}
}

// [tools] is the declarative "what"; [provision] stays the imperative "how".
// The generated apt install runs first, then the declared command, in one
// shell line so a failed install stops the sequence.
func TestToolsComposesWithProvision(t *testing.T) {
	c, err := contract.Parse(`
[tools]
packages = ["git", "curl"]

[provision]
command = "make setup"
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.HasProvision() {
		t.Fatal("tools + provision should have provision work")
	}
	want := "sudo -n apt-get update && sudo -n apt-get install -y git curl && make setup"
	if got := c.ProvisionCommand().String(); got != want {
		t.Fatalf("ProvisionCommand = %q, want %q", got, want)
	}
	if got := strings.Join(c.ProvisionCommand().Argv(), "\x00"); got != "/bin/sh\x00-c\x00"+want {
		t.Fatalf("ProvisionCommand argv = %q, want a shell string", got)
	}
}

// An argv provision command is exec'd with its argument boundaries intact
// when it is composed after the apt preamble.
func TestToolsComposesWithArgvProvision(t *testing.T) {
	c, err := contract.Parse(`
[tools]
packages = ["git"]

[provision]
command = ["make", "setup with space"]
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "sudo -n apt-get update && sudo -n apt-get install -y git && exec make 'setup with space'"
	if got := c.ProvisionCommand().String(); got != want {
		t.Fatalf("ProvisionCommand = %q, want %q", got, want)
	}
}

// A pinned package renders as apt's name=version spec.
func TestToolsPinnedVersionRendersAptSpec(t *testing.T) {
	c, err := contract.Parse(`
[tools]
packages = [{ name = "nodejs", version = "22.11.0" }]
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "sudo -n apt-get update && sudo -n apt-get install -y nodejs=22.11.0"
	if got := c.ProvisionCommand().String(); got != want {
		t.Fatalf("ProvisionCommand = %q, want %q", got, want)
	}
}

// With no [provision], the generated apt install is the whole provision.
func TestToolsOnlyIsTheWholeProvision(t *testing.T) {
	c, err := contract.Parse("[tools]\npackages = [\"git\"]\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.HasProvision() {
		t.Fatal("a tools-only contract has provision work")
	}
	if c.Empty() {
		t.Fatal("a tools-only contract is not empty")
	}
	if got := c.ProvisionTimeout(); got != contract.DefaultProvisionTimeout {
		t.Fatalf("provision timeout = %s, want the default", got)
	}
	want := "sudo -n apt-get update && sudo -n apt-get install -y git"
	if got := c.ProvisionCommand().String(); got != want {
		t.Fatalf("ProvisionCommand = %q, want %q", got, want)
	}
}

// The provision hook runs as the unprivileged box user, so the generated apt
// preamble must elevate through passwordless sudo. A contract's own [provision]
// command still runs as dev: only the preamble is prefixed with `sudo -n`.
func TestToolsPreambleUsesPasswordlessSudo(t *testing.T) {
	c, err := contract.Parse(`
[tools]
packages = ["git"]

[provision]
command = "make setup"
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := c.ProvisionCommand().String()
	want := "sudo -n apt-get update && sudo -n apt-get install -y git && make setup"
	if got != want {
		t.Fatalf("ProvisionCommand = %q, want %q", got, want)
	}
	const preamble = "sudo -n apt-get update && sudo -n apt-get install -y git && "
	if !strings.HasPrefix(got, preamble) {
		t.Fatalf("preamble must run apt through sudo -n: %q", got)
	}
	if tail := strings.TrimPrefix(got, preamble); tail != "make setup" {
		t.Fatalf("declared provision command = %q, want it unchanged", tail)
	}
}

// Existing contracts keep working: a [provision]-only contract's command is
// handed through unchanged, argv included.
func TestProvisionOnlyProvisionCommandUnchanged(t *testing.T) {
	c, err := contract.Parse("[provision]\ncommand = [\"make\", \"setup\"]\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(c.ProvisionCommand().Argv(), "\x00"); got != "make\x00setup" {
		t.Fatalf("ProvisionCommand argv = %q, want the declared argv", got)
	}

	shell, err := contract.Parse("[provision]\ncommand = \"make setup\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := shell.ProvisionCommand().String(); got != "make setup" {
		t.Fatalf("ProvisionCommand = %q, want the declared shell string", got)
	}
}

func TestContractWithoutProvisionHasNone(t *testing.T) {
	c, err := contract.Parse("[wake]\ncommand = \"true\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.HasProvision() {
		t.Fatal("a wake-only contract has no provision work")
	}
	if !c.ProvisionCommand().IsZero() {
		t.Fatalf("ProvisionCommand = %+v, want the zero command", c.ProvisionCommand())
	}
}

func TestToolsRejectedWhenMalformed(t *testing.T) {
	cases := map[string]string{
		"missing packages":    "[tools]\n",
		"empty packages":      "[tools]\npackages = []\n",
		"wrong package type":  "[tools]\npackages = [5]\n",
		"packages not a list": "[tools]\npackages = \"git\"\n",
		"uppercase name":      "[tools]\npackages = [\"Curl\"]\n",
		"name with a space":   "[tools]\npackages = [\"curl git\"]\n",
		"missing name":        "[tools]\npackages = [{ version = \"1\" }]\n",
		"unknown package key": "[tools]\npackages = [{ name = \"curl\", verison = \"1\" }]\n",
		"version with space":  "[tools]\npackages = [{ name = \"curl\", version = \"1 2\" }]\n",
		"unknown tools key":   "[tools]\npackages = [\"curl\"]\npackage = []\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.Parse(body); err == nil {
				t.Fatalf("Parse(%q) should fail", body)
			}
		})
	}
}

// A bad [tools] entry points at its line, like every other semantic failure.
func TestLoadPointsAtAToolsLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, contract.FileName)
	body := "[tools]\npackages = [\"Curl\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	_, err := contract.Load(dir)
	if err == nil {
		t.Fatal("Load should fail")
	}
	if want := fmt.Sprintf("%s:2:", path); !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if !errors.Is(err, contract.ErrInvalid) {
		t.Fatalf("error = %v, want it to match contract.ErrInvalid", err)
	}
}

// The contract is sent to the guest agent as JSON; [tools] must survive the
// trip and still compose.
func TestToolsRoundTripJSON(t *testing.T) {
	c, err := contract.Parse(`
[tools]
packages = ["git", { name = "nodejs", version = "22.11.0" }]

[provision]
command = ["make", "setup"]
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back contract.Contract
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Tools == nil || len(back.Tools.Packages) != 2 {
		t.Fatalf("tools after round trip = %+v", back.Tools)
	}
	if got := back.Tools.Packages[1]; got.Name != "nodejs" || got.Version != "22.11.0" {
		t.Fatalf("pinned package after round trip = %+v", got)
	}
	if back.ProvisionCommand().String() != c.ProvisionCommand().String() {
		t.Fatalf("composed command changed across JSON: %q vs %q", back.ProvisionCommand().String(), c.ProvisionCommand().String())
	}
}

// The tools declaration is part of the parsed values, so it moves the
// contract hash: editing it flags staleness like any other contract edit.
func TestToolsChangeTheContractHash(t *testing.T) {
	base, err := contract.Parse("[tools]\npackages = [\"git\"]\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	other, err := contract.Parse("[tools]\npackages = [\"git\", \"curl\"]\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if base.Hash() == other.Hash() {
		t.Fatal("adding a package should change the contract hash")
	}
	same, err := contract.Parse("[tools]\npackages = [\"git\"]\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if base.Hash() != same.Hash() {
		t.Fatal("the same tools should hash the same")
	}
}
