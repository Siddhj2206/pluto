package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
)

func TestWebhookAddListTestRemoveWithoutSecret(t *testing.T) {
	const secretEnv = "PLUTO_TEST_HOOK_SECRET"
	const secretValue = "super-secret-value"
	t.Setenv(secretEnv, secretValue)
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %+v, %v", boxes, err)
	}
	boxID := boxes[0].ID

	code, out, errOut := runCLI(t, "--socket", socket, "webhook", "add", "github",
		"--source", "production", "--box", boxID, "--secret-env", secretEnv)
	if code != 0 {
		t.Fatalf("webhook add exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "registered github source production") || !strings.Contains(out, "/github/production") {
		t.Fatalf("webhook add output = %q", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "webhook", "ls", "--json")
	if code != 0 {
		t.Fatalf("webhook ls exit = %d: %s", code, errOut)
	}
	var list api.WebhookListResponse
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode webhook ls: %v (%s)", err, out)
	}
	if len(list.Webhooks) != 1 {
		t.Fatalf("webhook list = %+v", list)
	}
	got := list.Webhooks[0]
	if got.Source != "production" || got.Endpoint != "/github/production" || !got.SecretPresent || got.SecretEnv != secretEnv {
		t.Fatalf("webhook status = %+v", got)
	}
	if strings.Contains(out, secretValue) {
		t.Fatalf("webhook ls returned the secret: %s", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "webhook", "ls")
	if code != 0 {
		t.Fatalf("webhook ls human exit = %d: %s", code, errOut)
	}
	for _, want := range []string{"SOURCE", "production", "/github/production", "present"} {
		if !strings.Contains(out, want) {
			t.Fatalf("webhook ls output = %q, want %q", out, want)
		}
	}

	code, out, errOut = runCLI(t, "--socket", socket, "webhook", "test", "production")
	if code != 0 {
		t.Fatalf("webhook test exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "test delivery") || !strings.Contains(out, "verified") {
		t.Fatalf("webhook test output = %q, want a labelled verified test delivery", out)
	}
	if strings.Contains(strings.ToLower(out), "github delivery") {
		t.Fatalf("webhook test output claims a GitHub delivery: %q", out)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "webhook", "rm", "production")
	if code != 0 {
		t.Fatalf("webhook rm exit = %d: %s", code, errOut)
	}
	code, out, _ = runCLI(t, "--socket", socket, "webhook", "ls")
	if code != 0 || !strings.Contains(out, "no webhook sources") {
		t.Fatalf("webhook ls after rm = %q (exit %d)", out, code)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "webhook", "add", "github",
		"--source", "missing", "--box", boxID, "--secret-env", "PLUTO_TEST_ABSENT_SECRET")
	if code == 0 {
		t.Fatal("webhook add accepted an unset secret environment variable")
	}
	if !strings.Contains(errOut, "PLUTO_TEST_ABSENT_SECRET") {
		t.Fatalf("webhook add stderr = %q, want the missing env name", errOut)
	}
}

func TestTrustShowsRevisionAndPolicyAndApproves(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	body := `[jobs.test]
command = 'go test ./...'

[events.push]
job = 'test'

[events.pull_request]
job = 'test'
actions = ['opened', 'synchronize']
`
	if err := os.WriteFile(filepath.Join(repo, contract.FileName), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	ct, err := contract.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %+v, %v", boxes, err)
	}
	box := boxes[0]

	code, out, errOut := runCLI(t, "--socket", socket, "trust", box.ID, "--yes")
	if code != 0 {
		t.Fatalf("trust exit = %d: %s", code, errOut)
	}
	for _, want := range []string{ct.Hash(), "jobs:", "test", "push -> test", "pull_request -> test", "approved"} {
		if !strings.Contains(out, want) {
			t.Fatalf("trust output = %q, want %q", out, want)
		}
	}
	approval, err := st.ContractApproval(box.Project)
	if err != nil || approval == nil || approval.Revision != ct.Hash() {
		t.Fatalf("approval = %+v, %v", approval, err)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "trust", box.ID, "--yes")
	if code != 0 || !strings.Contains(out, "already approved") {
		t.Fatalf("second trust = %q (exit %d, stderr %q)", out, code, errOut)
	}
}

func TestTrustRefusesWithoutConfirmation(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, contract.FileName), []byte("[jobs.test]\ncommand = 'true'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %+v, %v", boxes, err)
	}
	box := boxes[0]

	code, out, errOut := runCLIWithStdin(t, "n\n", "--socket", socket, "trust", box.ID)
	if code != 0 {
		t.Fatalf("declined trust exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "not approved") {
		t.Fatalf("declined trust output = %q", out)
	}
	if approval, err := st.ContractApproval(box.Project); err != nil || approval != nil {
		t.Fatalf("declined trust recorded an approval: %+v, %v", approval, err)
	}

	code, out, errOut = runCLIWithStdin(t, "y\n", "--socket", socket, "trust", box.ID)
	if code != 0 {
		t.Fatalf("confirmed trust exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "approved") {
		t.Fatalf("confirmed trust output = %q", out)
	}
	if approval, err := st.ContractApproval(box.Project); err != nil || approval == nil {
		t.Fatalf("confirmed trust approval = %+v, %v", approval, err)
	}
}

// runCLIWithStdin replaces the process stdin for one command, matching the
// setup-github test's approach to the interactive seam.
func runCLIWithStdin(t *testing.T, input string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	old := os.Stdin
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = old; _ = read.Close() })
	return runCLI(t, args...)
}
