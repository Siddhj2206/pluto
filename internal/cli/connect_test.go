package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

func TestConnectCLIPrintsStructuredServiceAccess(t *testing.T) {
	socket, st := startDaemonWith(t, fakeRunner{services: []state.ServiceStatus{{Name: "opencode", State: "active", Port: 4096}}})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, contract.FileName), []byte("[services.opencode]\ncommand = \"opencode serve\"\nport = 4096\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateBox("app", "main", dir); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "--socket", socket, "connect", dir, "opencode", "--json")
	if code != 0 {
		t.Fatalf("connect exit = %d; stderr: %s", code, stderr)
	}
	var result struct {
		Endpoint string `json:"endpoint"`
		Access   struct {
			Mode         string   `json:"mode"`
			Provider     string   `json:"provider"`
			Instructions []string `json:"instructions"`
		} `json:"access"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("connect output is not JSON: %v (%s)", err, stdout)
	}
	if result.Endpoint != "http://127.0.0.1:4096" || result.Access.Mode != "ssh-tunnel" || result.Access.Provider != "ssh" || len(result.Access.Instructions) == 0 {
		t.Fatalf("connect output = %+v", result)
	}
}

func TestConnectCLIRejectsMissingService(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{})
	code, _, stderr := runCLI(t, "--socket", socket, "connect")
	if code != 2 || !strings.Contains(stderr, "SERVICE") {
		t.Fatalf("connect missing args = (%d, %q); want usage error", code, stderr)
	}
}
