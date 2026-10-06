package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSensitiveJobUnitLivesOnlyInRuntimeDirectory(t *testing.T) {
	homeUnits := t.TempDir()
	runtime := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "systemd", "user")
	secretUnit := "pluto-job-secret-test-" + strconv.Itoa(os.Getpid()) + ".service"
	contents := []byte("[Service]\nEnvironment=TOKEN=host-secret\n")
	cleanup, err := installJobUnit(homeUnits, runtime, secretUnit, contents, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	link := filepath.Join(homeUnits, secretUnit)
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("secret unit unexpectedly exists in durable unit path: %v", err)
	}
	target := filepath.Join(runtime, secretUnit)
	if _, err := os.ReadFile(target); err != nil {
		t.Fatalf("runtime unit missing: %v", err)
	}
	cleanup()
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("volatile secret unit remains after run: %v", err)
	}
}
