package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
)

const adrExample = `
[box]
image = "ubuntu-24.04"
resources = { cpus = 4, memory = "8GiB", disk = "40GiB" }

[provision]
command = ".pluto/provision.sh"
timeout = "20m"

[wake]
command = ".pluto/wake.sh"
timeout = "30s"

[services.dev]
command = "pnpm dev"
port = 3000

[[schedule]]
name = "nightly"
cron = "0 2 * * *"
command = "pnpm test"
`

func TestParseADRExample(t *testing.T) {
	c, err := contract.Parse(adrExample)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Box.Image != "ubuntu-24.04" {
		t.Fatalf("image = %q", c.Box.Image)
	}
	if c.Box.Resources.CPUs != 4 || c.Box.Resources.Memory != "8GiB" {
		t.Fatalf("resources = %+v", c.Box.Resources)
	}
	if c.Provision == nil || c.Provision.Command != ".pluto/provision.sh" {
		t.Fatalf("provision = %+v", c.Provision)
	}
	if got := c.ProvisionTimeout(); got != 20*time.Minute {
		t.Fatalf("provision timeout = %s", got)
	}
	if c.Wake == nil || c.Wake.Command != ".pluto/wake.sh" {
		t.Fatalf("wake = %+v", c.Wake)
	}
	if got := c.WakeTimeout(); got != 30*time.Second {
		t.Fatalf("wake timeout = %s", got)
	}
	dev, ok := c.Services["dev"]
	if !ok || dev.Command != "pnpm dev" || dev.Port != 3000 {
		t.Fatalf("services = %+v", c.Services)
	}
	if len(c.Schedules) != 1 || c.Schedules[0].Name != "nightly" {
		t.Fatalf("schedules = %+v", c.Schedules)
	}
}

func TestLoadMissingFileIsEmptyContract(t *testing.T) {
	c, err := contract.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Empty() {
		t.Fatalf("contract should be empty: %+v", c)
	}
	if c.ProvisionTimeout() != contract.DefaultProvisionTimeout {
		t.Fatalf("default provision timeout = %s", c.ProvisionTimeout())
	}
	if c.WakeTimeout() != contract.DefaultWakeTimeout {
		t.Fatalf("default wake timeout = %s", c.WakeTimeout())
	}
}

func TestPhasesWithoutTimeoutsUseDefaults(t *testing.T) {
	c, err := contract.Parse("[provision]\ncommand = \"true\"\n[wake]\ncommand = \"true\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.ProvisionTimeout() != contract.DefaultProvisionTimeout {
		t.Fatalf("provision timeout = %s, want the default", c.ProvisionTimeout())
	}
	if c.WakeTimeout() != contract.DefaultWakeTimeout {
		t.Fatalf("wake timeout = %s, want the default", c.WakeTimeout())
	}
}

func TestLoadReadsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, contract.FileName), []byte("[wake]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := contract.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Wake == nil || c.Wake.Command != "true" {
		t.Fatalf("wake = %+v", c.Wake)
	}
}

func TestParseRejectsBadContracts(t *testing.T) {
	cases := map[string]string{
		"missing command":         "[provision]\ntimeout = \"5m\"\n",
		"bad timeout":             "[wake]\ncommand = \"true\"\ntimeout = \"soon\"\n",
		"unknown key":             "[wake]\ncommand = \"true\"\ncomand = \"oops\"\n",
		"bad service name":        "[services.\"bad name\"]\ncommand = \"x\"\n",
		"bad port":                "[services.web]\ncommand = \"x\"\nport = 99999\n",
		"missing service command": "[services.web]\nport = 3000\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.Parse(body); err == nil {
				t.Fatalf("Parse(%q) should fail", body)
			}
		})
	}
}

func TestServiceNamesSorted(t *testing.T) {
	c, err := contract.Parse("[services.web]\ncommand = \"x\"\n[services.db]\ncommand = \"y\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	names := c.ServiceNames()
	if strings.Join(names, ",") != "db,web" {
		t.Fatalf("names = %v, want db,web", names)
	}
}
