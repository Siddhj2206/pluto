package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

func TestSensitiveJobOutputIsRedactedBeforeDurableLogAndStream(t *testing.T) {
	root := t.TempDir()
	system := newFakeSystem()
	system.jobChunks = []string{"token=host-", "secret-value\n"}
	ag, err := New(root, system)
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	job, err := ag.RunJob(state.NewID(), contract.Exec{Command: contract.ArgvCommand([]string{"echo"}), Env: map[string]string{"PRIVATE_TOKEN": "host-secret-value"}, SensitiveEnv: []string{"PRIVATE_TOKEN"}}, "/work", func(data []byte) { streamed.Write(data) })
	if err != nil {
		t.Fatal(err)
	}
	if job.State != state.JobDone {
		t.Fatalf("job=%+v", job)
	}
	log, err := os.ReadFile(ag.jobLogPath(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	for where, value := range map[string]string{"durable log": string(log), "stream": streamed.String()} {
		if strings.Contains(value, "host-secret-value") {
			t.Fatalf("raw secret leaked to %s: %q", where, value)
		}
		if !strings.Contains(value, "[REDACTED]") {
			t.Fatalf("secret not redacted in %s: %q", where, value)
		}
	}
	if _, err := os.Stat(sensitiveJobLogPath(job.ID)); !os.IsNotExist(err) {
		t.Fatalf("raw tmpfs log remains: %v", err)
	}
}
