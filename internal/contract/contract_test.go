package contract_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// adrExample is the canonical v2 contract from ADR 0007 (#39).
const adrExample = `
[box]
image = "ubuntu-24.04"
resources = { cpus = 4, memory = "8GiB", disk = "40GiB" }
auto_pause = "1h"

[env]
NODE_ENV = "development"

[provision]
command = ".pluto/provision.sh"
timeout = "20m"

[wake]
command = ".pluto/wake.sh"
timeout = "30s"

[jobs.dev]
description = "start the dev server"
command = ["pnpm", "dev"]
dir = "web"

[jobs.test]
description = "run the test suite"
command = "pnpm test"
timeout = "30m"

[services.web]
description = "web UI"
command = "pnpm dev"
port = 3000

[sessions.agent]
description = "the coding agent"
command = "opencode"

[[schedule]]
name = "nightly"
cron = "0 2 * * *"
job = "test"
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
	if c.Provision == nil || c.Provision.Command.String() != ".pluto/provision.sh" {
		t.Fatalf("provision = %+v", c.Provision)
	}
	if got := c.ProvisionTimeout(); got != 20*time.Minute {
		t.Fatalf("provision timeout = %s", got)
	}
	if c.Wake == nil || c.Wake.Command.String() != ".pluto/wake.sh" {
		t.Fatalf("wake = %+v", c.Wake)
	}
	if got := c.WakeTimeout(); got != 30*time.Second {
		t.Fatalf("wake timeout = %s", got)
	}
	dev, ok := c.Jobs["dev"]
	if !ok || dev.Description != "start the dev server" || strings.Join(dev.Command.Argv(), " ") != "pnpm dev" || dev.Dir != "web" {
		t.Fatalf("jobs.dev = %+v", dev)
	}
	test, ok := c.Jobs["test"]
	if !ok || test.Command.String() != "pnpm test" || test.Timeout != "30m" {
		t.Fatalf("jobs.test = %+v", test)
	}
	web, ok := c.Services["web"]
	if !ok || web.Command.String() != "pnpm dev" || web.Port != 3000 || web.Description != "web UI" {
		t.Fatalf("services = %+v", c.Services)
	}
	agent, ok := c.Sessions["agent"]
	if !ok || agent.Command.String() != "opencode" || agent.Description != "the coding agent" {
		t.Fatalf("sessions = %+v", c.Sessions)
	}
	if len(c.Schedules) != 1 || c.Schedules[0].Name != "nightly" || c.Schedules[0].Job != "test" {
		t.Fatalf("schedules = %+v", c.Schedules)
	}
}

func TestCommandStringRunsViaShell(t *testing.T) {
	c, err := contract.Parse("[jobs.a]\ncommand = \"make test && echo ok\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cmd := c.Jobs["a"].Command
	if cmd.IsZero() {
		t.Fatal("command should not be zero")
	}
	if got := cmd.String(); got != "make test && echo ok" {
		t.Fatalf("String = %q, want the declared shell string", got)
	}
	if got := strings.Join(cmd.Argv(), "\x00"); got != "/bin/sh\x00-c\x00make test && echo ok" {
		t.Fatalf("Argv = %q, want /bin/sh -c <string>", got)
	}
}

func TestCommandArrayIsExecdDirectly(t *testing.T) {
	c, err := contract.Parse("[jobs.a]\ncommand = [\"pnpm\", \"dev\", \"--host\"]\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cmd := c.Jobs["a"].Command
	if got := strings.Join(cmd.Argv(), "\x00"); got != "pnpm\x00dev\x00--host" {
		t.Fatalf("Argv = %q, want the declared argv", got)
	}
	if got := cmd.String(); got != "pnpm dev --host" {
		t.Fatalf("String = %q, want the argv joined for display", got)
	}
}

func TestCommandRoundTripsJSON(t *testing.T) {
	src := []contract.Command{
		contract.ShellCommand("pnpm test"),
		contract.ArgvCommand([]string{"pnpm", "dev", "a b"}),
	}
	for _, cmd := range src {
		data, err := json.Marshal(cmd)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", cmd, err)
		}
		var back contract.Command
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if back.String() != cmd.String() || strings.Join(back.Argv(), "\x00") != strings.Join(cmd.Argv(), "\x00") {
			t.Fatalf("round trip %s = %q / %v, want %q / %v", data, back.String(), back.Argv(), cmd.String(), cmd.Argv())
		}
	}
	// The declared shape is preserved, not normalized to one form.
	data, _ := json.Marshal(contract.ShellCommand("pnpm test"))
	if string(data) != `"pnpm test"` {
		t.Fatalf("shell command JSON = %s, want a string", data)
	}
}

func TestCommandRejectsBadShapes(t *testing.T) {
	cases := map[string]string{
		"empty string":  "[jobs.a]\ncommand = \"\"\n",
		"empty array":   "[jobs.a]\ncommand = []\n",
		"number":        "[jobs.a]\ncommand = 5\n",
		"non-string":    "[jobs.a]\ncommand = [\"ok\", 5]\n",
		"missing jobs":  "[jobs.a]\ndescription = \"x\"\n",
		"phase empty":   "[wake]\ncommand = []\n",
		"service empty": "[services.web]\ncommand = \"\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.Parse(body); err == nil {
				t.Fatalf("Parse(%q) should fail", body)
			}
		})
	}
}

func TestEnvMergesPerKeyOverTheTopLevel(t *testing.T) {
	c, err := contract.Parse(`
[env]
FOO = "top"
BAR = "top"

[provision]
command = "true"
env = { BAR = "provision", BAZ = "provision" }

[jobs.dev]
command = "true"
env = { BAR = "job" }
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := c.EnvFor(c.Provision.Env)
	want := map[string]string{"FOO": "top", "BAR": "provision", "BAZ": "provision"}
	if len(got) != len(want) {
		t.Fatalf("provision env = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("provision env[%s] = %q, want %q", k, got[k], v)
		}
	}

	exec, err := c.ExecJob("dev")
	if err != nil {
		t.Fatalf("ExecJob: %v", err)
	}
	if exec.Env["FOO"] != "top" || exec.Env["BAR"] != "job" {
		t.Fatalf("job env = %v, want FOO=top BAR=job", exec.Env)
	}
}

func TestReservedPlutoEnvRejected(t *testing.T) {
	cases := map[string]string{
		"top level":  "[env]\nPLUTO_WORKTREE = \"/nope\"\n",
		"provision":  "[provision]\ncommand = \"true\"\nenv = { PLUTO_X = \"1\" }\n",
		"service":    "[services.web]\ncommand = \"x\"\nenv = { PLUTO_X = \"1\" }\n",
		"job":        "[jobs.a]\ncommand = \"x\"\nenv = { PLUTO_X = \"1\" }\n",
		"empty key":  "[env]\n\"\" = \"x\"\n",
		"multiline":  "[env]\nFOO = \"one\\ntwo\"\n",
		"equals key": "[env]\n\"A=B\" = \"x\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.Parse(body); err == nil {
				t.Fatalf("Parse(%q) should fail", body)
			}
		})
	}
}

func TestExecJobResolvesCommandDirEnvAndTimeout(t *testing.T) {
	c, err := contract.Parse(`
[env]
NODE_ENV = "development"

[jobs.dev]
command = ["pnpm", "dev"]
dir = "web"

[jobs.test]
command = "pnpm test"
timeout = "30m"
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dev, err := c.ExecJob("dev")
	if err != nil {
		t.Fatalf("ExecJob: %v", err)
	}
	if strings.Join(dev.Command.Argv(), " ") != "pnpm dev" || dev.Dir != "web" || dev.Env["NODE_ENV"] != "development" || dev.Timeout != 0 {
		t.Fatalf("dev exec = %+v", dev)
	}
	test, err := c.ExecJob("test")
	if err != nil {
		t.Fatalf("ExecJob: %v", err)
	}
	if test.Timeout != 30*time.Minute {
		t.Fatalf("test timeout = %s, want 30m", test.Timeout)
	}
}

func TestExecJobUnknownListsDeclaredJobsWithDescriptions(t *testing.T) {
	c, err := contract.Parse(`
[jobs.dev]
description = "start the dev server"
command = ["pnpm", "dev"]

[jobs.test]
command = "pnpm test"
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = c.ExecJob("web")
	if err == nil {
		t.Fatal("ExecJob(web) should fail")
	}
	if !errors.Is(err, contract.ErrNoSuchJob) {
		t.Fatalf("error = %v, want ErrNoSuchJob", err)
	}
	for _, want := range []string{"web", "dev (start the dev server)", "test"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}

	empty, err := contract.Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = empty.ExecJob("web")
	if err == nil || !strings.Contains(err.Error(), "no jobs declared") {
		t.Fatalf("empty error = %v, want 'no jobs declared'", err)
	}
}

func TestAdHocExecUsesTheTopLevelEnv(t *testing.T) {
	c, err := contract.Parse("[env]\nFOO = \"bar\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	exec := c.AdHocExec([]string{"make", "test"})
	if strings.Join(exec.Command.Argv(), " ") != "make test" || exec.Command.String() != "make test" {
		t.Fatalf("ad-hoc command = %+v", exec.Command)
	}
	if exec.Env["FOO"] != "bar" || exec.Timeout != 0 || exec.Dir != "" {
		t.Fatalf("ad-hoc exec = %+v", exec)
	}
}

func TestJobsValidated(t *testing.T) {
	cases := map[string]string{
		"bad name":        "[jobs.\"bad name\"]\ncommand = \"x\"\n",
		"missing command": "[jobs.dev]\ndescription = \"x\"\n",
		"bad timeout":     "[jobs.dev]\ncommand = \"x\"\ntimeout = \"soon\"\n",
		"zero timeout":    "[jobs.dev]\ncommand = \"x\"\ntimeout = \"0s\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.Parse(body); err == nil {
				t.Fatalf("Parse(%q) should fail", body)
			}
		})
	}
}

func TestJobNamesSorted(t *testing.T) {
	c, err := contract.Parse("[jobs.web]\ncommand = \"x\"\n[jobs.db]\ncommand = \"y\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(c.JobNames(), ","); got != "db,web" {
		t.Fatalf("JobNames = %q, want db,web", got)
	}
}

func TestSchedulesValidateJobCronAndNames(t *testing.T) {
	warmup, err := contract.Parse(`
[jobs.test]
command = "true"

[[schedule]]
name = "keep-warm"
cron = "*/10 * * * *"
`)
	if err != nil {
		t.Fatalf("a schedule without a job is a warm-up: %v", err)
	}
	if warmup.Schedules[0].Job != "" {
		t.Fatalf("schedule job = %q, want empty", warmup.Schedules[0].Job)
	}

	cases := map[string]string{
		"no name":         "[[schedule]]\ncron = \"0 2 * * *\"\n",
		"no cron":         "[[schedule]]\nname = \"nightly\"\n",
		"bad cron":        "[[schedule]]\nname = \"nightly\"\ncron = \"0 2 * *\"\n",
		"bad cron range":  "[[schedule]]\nname = \"nightly\"\ncron = \"0 25 * * *\"\n",
		"unknown job":     "[[schedule]]\nname = \"nightly\"\ncron = \"0 2 * * *\"\njob = \"test\"\n",
		"duplicate names": "[jobs.test]\ncommand = \"true\"\n[[schedule]]\nname = \"nightly\"\ncron = \"0 2 * * *\"\njob = \"test\"\n[[schedule]]\nname = \"nightly\"\ncron = \"0 3 * * *\"\njob = \"test\"\n",
		"inline command":  "[[schedule]]\nname = \"nightly\"\ncron = \"0 2 * * *\"\ncommand = \"true\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.Parse(body); err == nil {
				t.Fatalf("Parse(%q) should fail", body)
			}
		})
	}
}

func TestResolveDirDefaultsToTheWorktreeRoot(t *testing.T) {
	if got := contract.ResolveDir("/home/dev/work/app", ""); got != "/home/dev/work/app" {
		t.Fatalf("ResolveDir(empty) = %q, want the root", got)
	}
	if got := contract.ResolveDir("/home/dev/work/app", "web"); got != "/home/dev/work/app/web" {
		t.Fatalf("ResolveDir(web) = %q, want it under the root", got)
	}
	if got := contract.ResolveDir("/home/dev/work/app", "/opt/data"); got != "/opt/data" {
		t.Fatalf("ResolveDir(absolute) = %q, want it kept", got)
	}
}

func TestMergeEnvCopiesAndOverrides(t *testing.T) {
	base := map[string]string{"A": "1", "B": "2"}
	over := map[string]string{"B": "3"}
	got := contract.MergeEnv(base, over)
	if got["A"] != "1" || got["B"] != "3" {
		t.Fatalf("merge = %v", got)
	}
	got["A"] = "mutated"
	if base["A"] != "1" {
		t.Fatal("MergeEnv should copy its inputs")
	}
	if contract.MergeEnv(nil, nil) != nil {
		t.Fatal("merging nothing should be nil")
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

func TestContractWithJobsIsNotEmpty(t *testing.T) {
	c, err := contract.Parse("[jobs.dev]\ncommand = \"true\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Empty() {
		t.Fatal("a contract with declared jobs should not be empty")
	}
}

func TestAutoPauseWindowDefaultsToAnHour(t *testing.T) {
	c, err := contract.Parse("[wake]\ncommand = \"true\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.AutoPauseWindow(); got != contract.DefaultAutoPause {
		t.Fatalf("auto-pause window = %s, want the default %s", got, contract.DefaultAutoPause)
	}
	if contract.DefaultAutoPause != time.Hour {
		t.Fatalf("DefaultAutoPause = %s, want 1h (ADR 0002)", contract.DefaultAutoPause)
	}
}

func TestAutoPauseWindowFromContract(t *testing.T) {
	c, err := contract.Parse("[box]\nauto_pause = \"30m\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.AutoPauseWindow(); got != 30*time.Minute {
		t.Fatalf("auto-pause window = %s, want 30m", got)
	}
}

func TestAutoPauseOffDisables(t *testing.T) {
	c, err := contract.Parse("[box]\nauto_pause = \"off\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.AutoPauseWindow(); got != 0 {
		t.Fatalf("auto-pause window = %s, want 0 (disabled)", got)
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
	if c.Wake == nil || c.Wake.Command.String() != "true" {
		t.Fatalf("wake = %+v", c.Wake)
	}
}

// ADR 0009 + docs/contract.md: a semantic contract failure points at the
// line of the key it blames, best effort, and classifies as ErrInvalid so
// the daemon and CLI can turn it into the edit hint.
func TestLoadPointsAtTheBlamedLine(t *testing.T) {
	cases := []struct {
		name string
		body string
		line int
	}{
		{"invalid job timeout", "[jobs.dev]\ncommand = \"make\"\ntimeout = \"soon\"\n", 3},
		{"unknown key", "[jobs.dev]\ncommand = \"make\"\ndescriptionn = \"typo\"\n", 3},
		{"missing command falls back to the section", "[provision]\ntimeout = \"5m\"\n", 1},
		{"reserved env prefix", "[env]\nPLUTO_X = \"1\"\n", 2},
		{"bad cron", "[[schedule]]\nname = \"nightly\"\ncron = \"nope\"\n", 3},
		{"unknown job", "[[schedule]]\nname = \"nightly\"\ncron = \"0 2 * * *\"\njob = \"missing\"\n", 4},
		{"missing session command falls back to the section", "[sessions.agent]\ndescription = \"x\"\n", 1},
		{"bad session name", "[sessions.\"bad name\"]\ncommand = \"x\"\n", 1},
		{"unknown session key", "[sessions.agent]\ncommand = \"x\"\nbogus = \"y\"\n", 3},
		{"duplicate session table", "[sessions.agent]\ncommand = \"a\"\n[sessions.agent]\ncommand = \"b\"\n", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, contract.FileName)
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write contract: %v", err)
			}
			_, err := contract.Load(dir)
			if err == nil {
				t.Fatal("Load should fail")
			}
			if want := fmt.Sprintf("%s:%d:", path, tc.line); !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want %q", err, want)
			}
			if !errors.Is(err, contract.ErrInvalid) {
				t.Fatalf("error = %v, want it to match contract.ErrInvalid", err)
			}
		})
	}
}

// A syntax error keeps the parser's position.
func TestLoadKeepsSyntaxPositions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, contract.FileName)
	if err := os.WriteFile(path, []byte("[jobs.dev]\ncommand = [\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	_, err := contract.Load(dir)
	if err == nil || !strings.Contains(err.Error(), path+":") {
		t.Fatalf("error = %v, want file:line", err)
	}
	if !errors.Is(err, contract.ErrInvalid) {
		t.Fatalf("error = %v, want it to match contract.ErrInvalid", err)
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
		"bad auto_pause":          "[box]\nauto_pause = \"soon\"\n",
		"zero auto_pause":         "[box]\nauto_pause = \"0s\"\n",
		"unknown job key":         "[jobs.dev]\ncommand = \"x\"\ndescriptionn = \"typo\"\n",
		"unknown schedule key":    "[[schedule]]\nname = \"n\"\ncron = \"0 2 * * *\"\ncommand = \"x\"\n",
		"bad session name":        "[sessions.\"bad name\"]\ncommand = \"x\"\n",
		"missing session command": "[sessions.agent]\ndescription = \"x\"\n",
		"unknown session key":     "[sessions.agent]\ncommand = \"x\"\nbogus = \"y\"\n",
		"duplicate session name":  "[sessions.agent]\ncommand = \"a\"\n[sessions.agent]\ncommand = \"b\"\n",
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

func TestPullRequestPolicyFiltersConfiguredActions(t *testing.T) {
	c, err := contract.Parse("[jobs.test]\ncommand='true'\n[events.pull_request]\njob='test'\nactions=['opened','synchronize']\n")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Events.PullRequest.Allows("opened") || !c.Events.PullRequest.Allows("synchronize") || c.Events.PullRequest.Allows("closed") {
		t.Fatalf("pull request actions were not filtered: %+v", c.Events.PullRequest)
	}
}

func TestIssuePolicyFiltersConfiguredActions(t *testing.T) {
	c, err := contract.Parse("[jobs.issue-work]\ncommand='true'\n[events.issue]\njob='issue-work'\nactions=['opened','labeled']\ncredentials=['ISSUE_TOKEN']\n")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Events.Issue.Allows("opened") || !c.Events.Issue.Allows("labeled") || c.Events.Issue.Allows("closed") {
		t.Fatalf("issue actions were not filtered: %+v", c.Events.Issue)
	}
	if c.Events.Issue.Job != "issue-work" || len(c.Events.Issue.CredentialNames) != 1 || c.Events.Issue.CredentialNames[0] != "ISSUE_TOKEN" {
		t.Fatalf("issue policy was not parsed: %+v", c.Events.Issue)
	}
}
