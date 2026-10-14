package contract_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// pluto.yml is the box contract (#139). A project declares provision, wake,
// box capability, secrets and masking, named tasks, and schedules. Every node
// may be inline or a reference to a typed YAML file; both forms compile to one
// internal execution model.

func TestParseProjectLoadsInlineContract(t *testing.T) {
	p, err := contract.ParseProject(`
version: 1

provision:
  base: ubuntu:24.04
  packages: [build-essential, nodejs]
  watch: [Cargo.lock, pnpm-lock.yaml]
  run: |
    corepack enable

docker: true

secrets:
  REGISTRY_TOKEN: { env: REGISTRY_TOKEN }
mask: ["ghp_", "AKIA"]

tasks:
  dev:   { run: ./scripts/dev.sh, lock: shared }
  build: { run: ["./scripts/build.sh", "make test"], lock: exclusive }
  db:    { service: true, run: "postgres -D /workspace/.pg" }

schedules:
  nightly: { task: build, cron: "0 3 * * *" }
`)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	if p.Version != 1 {
		t.Fatalf("version = %d, want 1", p.Version)
	}
	if p.Provision == nil {
		t.Fatal("provision not loaded")
	}
	if p.Provision.Base.Kind != contract.BaseImage || p.Provision.Base.Image != "ubuntu:24.04" {
		t.Fatalf("provision.base = %+v, want the ubuntu OCI reference", p.Provision.Base)
	}
	if got := strings.Join(p.Provision.Packages, ","); got != "build-essential,nodejs" {
		t.Fatalf("provision.packages = %v", p.Provision.Packages)
	}
	if got := strings.Join(p.Provision.Watch, ","); got != "Cargo.lock,pnpm-lock.yaml" {
		t.Fatalf("provision.watch = %v", p.Provision.Watch)
	}
	if strings.TrimSpace(p.Provision.Run.String()) != "corepack enable" {
		t.Fatalf("provision.run = %q", p.Provision.Run.String())
	}
	if !p.Docker {
		t.Fatal("docker = false, want true")
	}
	if got := p.Secrets["REGISTRY_TOKEN"].Env; got != "REGISTRY_TOKEN" {
		t.Fatalf("secrets.REGISTRY_TOKEN.env = %q", got)
	}
	if got := strings.Join(p.Mask, ","); got != "ghp_,AKIA" {
		t.Fatalf("mask = %v", p.Mask)
	}
	if task := p.Tasks["dev"]; task.Lock != contract.LockShared || task.Run.String() != "./scripts/dev.sh" {
		t.Fatalf("tasks.dev = %+v", task)
	}
	if task := p.Tasks["build"]; task.Lock != contract.LockExclusive {
		t.Fatalf("tasks.build.lock = %q, want exclusive", task.Lock)
	} else if got := strings.Join(task.Run.Argv(), " "); got != "./scripts/build.sh make test" {
		t.Fatalf("tasks.build.run argv = %q", got)
	}
	if task := p.Tasks["db"]; !task.Service {
		t.Fatalf("tasks.db = %+v, want service", task)
	}
	if sched := p.Schedules["nightly"]; sched.Task != "build" || sched.Cron != "0 3 * * *" {
		t.Fatalf("schedules.nightly = %+v", sched)
	}
}

func TestParseProjectRejectsMissingVersion(t *testing.T) {
	_, err := contract.ParseProject("tasks:\n  test: { run: \"true\" }\n")
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err = %v, want a version complaint", err)
	}
}

// writeTree writes files under a temp root. Paths ending in "/" are
// directories; every other path is a file with the given body.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const referencedContract = `version: 1
provision: ./pluto/provision.yml
wake: ./pluto/wake.yml
tasks:
  dev: ./pluto/tasks/dev.yml
`

func TestLoadProjectResolvesTypedFileReferences(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"pluto.yml":           referencedContract,
		"pluto/provision.yml": "base: { dockerfile: ./pluto/Dockerfile }\npackages: [nodejs]\nrun: ./pluto/provision.sh\n",
		"pluto/wake.yml":      "run: ./scripts/wake.sh\n",
		"pluto/tasks/dev.yml": "run: ./scripts/dev.sh\nlock: shared\n",
	})
	p, err := contract.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if p.Provision == nil || p.Provision.Base.Kind != contract.BaseDockerfile || p.Provision.Base.Dockerfile != "./pluto/Dockerfile" {
		t.Fatalf("provision.base = %+v", p.Provision)
	}
	if p.Wake == nil || p.Wake.Run.String() != "./scripts/wake.sh" {
		t.Fatalf("wake = %+v", p.Wake)
	}
	if task := p.Tasks["dev"]; task.Lock != contract.LockShared || task.Run.String() != "./scripts/dev.sh" {
		t.Fatalf("tasks.dev = %+v", task)
	}
}

// Every accepted form compiles to one internal execution model: a contract
// written entirely inline and the same contract split across typed files must
// produce equal values and the same fingerprint.
func TestInlineAndReferencedFormsCompileToOneModel(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"pluto.yml":           referencedContract,
		"pluto/provision.yml": "base: ubuntu:24.04\npackages: [nodejs]\nwatch: [pnpm-lock.yaml]\n",
		"pluto/wake.yml":      "run: ./scripts/wake.sh\n",
		"pluto/tasks/dev.yml": "run: ./scripts/dev.sh\nlock: shared\n",
	})
	inline, err := contract.ParseProject(`version: 1
provision:
  base: ubuntu:24.04
  packages: [nodejs]
  watch: [pnpm-lock.yaml]
wake:
  run: ./scripts/wake.sh
tasks:
  dev: { run: ./scripts/dev.sh, lock: shared }
`)
	if err != nil {
		t.Fatalf("ParseProject(inline): %v", err)
	}
	fromFiles, err := contract.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if inline.Hash() != fromFiles.Hash() {
		t.Fatalf("hashes differ: inline %s, referenced %s", inline.Hash(), fromFiles.Hash())
	}
	if !reflect.DeepEqual(inline, fromFiles) {
		t.Fatalf("models differ:\n inline: %+v\n files:  %+v", inline, fromFiles)
	}
}

func TestLoadProjectNamesAMissingReferencedFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"pluto.yml": "version: 1\nprovision: ./pluto/provision.yml\n"})
	_, err := contract.LoadProject(dir)
	if err == nil || !strings.Contains(err.Error(), "provision.yml") {
		t.Fatalf("err = %v, want the missing referenced file named", err)
	}
}

func TestLoadProjectNamesAMalformedReferencedFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"pluto.yml":      "version: 1\nwake: ./pluto/wake.yml\n",
		"pluto/wake.yml": "run: [unterminated\n",
	})
	_, err := contract.LoadProject(dir)
	if err == nil || !strings.Contains(err.Error(), "wake.yml") {
		t.Fatalf("err = %v, want the malformed referenced file named", err)
	}
	if !strings.Contains(err.Error(), ": line ") && !strings.Contains(err.Error(), "wake.yml:") {
		t.Fatalf("err = %v, want a line for the malformed reference", err)
	}
}

func TestLoadProjectRejectsAReferenceThatIsNotAMapping(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"pluto.yml":      "version: 1\nwake: ./pluto/wake.yml\n",
		"pluto/wake.yml": "- one\n- two\n",
	})
	_, err := contract.LoadProject(dir)
	if err == nil || !strings.Contains(err.Error(), "wake.yml") || !strings.Contains(err.Error(), "mapping") {
		t.Fatalf("err = %v, want a mapping complaint naming the file", err)
	}
}

func TestParseProjectNamesTheOffendingField(t *testing.T) {
	_, err := contract.ParseProject("version: 1\ntasks:\n  build:\n    run: make test\n    lock: solo\n")
	if err == nil || !strings.Contains(err.Error(), "tasks.build.lock") {
		t.Fatalf("err = %v, want tasks.build.lock named", err)
	}
	_, err = contract.ParseProject("version: 1\ntasks:\n  build:\n    runn: make test\n")
	if err == nil || !strings.Contains(err.Error(), "runn") {
		t.Fatalf("err = %v, want the unknown field named", err)
	}
}

func TestParseProjectRejectsInvalidContracts(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"bad version", "version: 2\n", "version"},
		{"no version", "tasks:\n  dev: { run: a }\n", "version"},
		{"provision without base", "version: 1\nprovision:\n  packages: [a]\n", "provision.base"},
		{"empty package", "version: 1\nprovision:\n  base: ubuntu\n  packages: [''\n  ]\n", "provision.packages"},
		{"wake without run", "version: 1\nwake: { }\n", "wake.run"},
		{"service with lock", "version: 1\ntasks:\n  db: { run: a, service: true, lock: exclusive }\n", "tasks.db"},
		{"secret with neither", "version: 1\nsecrets:\n  X: { }\n", "secrets.X"},
		{"secret with both", "version: 1\nsecrets:\n  X: { env: A, file: b }\n", "secrets.X"},
		{"reserved secret name", "version: 1\nsecrets:\n  PLUTO_X: { env: A }\n", "secrets.PLUTO_X"},
		{"empty mask entry", "version: 1\nmask: ['']\n", "mask"},
		{"task without run", "version: 1\ntasks:\n  dev: { lock: shared }\n", "tasks.dev.run"},
		{"bad task name", "version: 1\ntasks:\n  'a b': { run: a }\n", "tasks.a b"},
		{"schedule without task", "version: 1\nschedules:\n  s: { cron: '0 0 * * *' }\n", "schedules.s.task"},
		{"schedule unknown task", "version: 1\nschedules:\n  s: { task: nope, cron: '0 0 * * *' }\n", "schedules.s.task"},
		{"schedule bad cron", "version: 1\ntasks:\n  dev: { run: a }\nschedules:\n  s: { task: dev, cron: 'nope' }\n", "schedules.s.cron"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := contract.ParseProject(tc.src)
			if err == nil {
				t.Fatalf("parsed %q, want an error naming %q", tc.src, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

func TestProjectResolvesEffectiveLockPolicy(t *testing.T) {
	p, err := contract.ParseProject("version: 1\ntasks:\n  dev: { run: a }\n  build: { run: b, lock: exclusive }\n  db: { run: c, service: true }\n")
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	if got := p.Tasks["dev"].EffectiveLock(); got != contract.LockShared {
		t.Fatalf("dev lock = %q, want the shared default", got)
	}
	if got := p.Tasks["build"].EffectiveLock(); got != contract.LockExclusive {
		t.Fatalf("build lock = %q", got)
	}
	if got := p.Tasks["db"].EffectiveLock(); got != contract.LockService {
		t.Fatalf("db lock = %q, want service", got)
	}
	if got := strings.Join(p.TaskNames(), ","); got != "build,db,dev" {
		t.Fatalf("TaskNames = %q", got)
	}
}

func TestProjectResolvesLockAndServicesByName(t *testing.T) {
	p, err := contract.ParseProject("version: 1\ntasks:\n  dev: { run: a }\n  build: { run: b, lock: exclusive }\n  db: { run: c, service: true }\n")
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	for name, want := range map[string]contract.Lock{"dev": contract.LockShared, "build": contract.LockExclusive, "db": contract.LockService} {
		got, err := p.TaskLock(name)
		if err != nil {
			t.Fatalf("TaskLock(%s): %v", name, err)
		}
		if got != want {
			t.Fatalf("TaskLock(%s) = %q, want %q", name, got, want)
		}
	}
	if _, err := p.TaskLock("nope"); err == nil || !errors.Is(err, contract.ErrNoSuchTask) {
		t.Fatalf("TaskLock(nope) error = %v, want ErrNoSuchTask", err)
	}
	services := p.ServiceTasks()
	if len(services) != 1 || services[0].Name != "db" || strings.Join(services[0].Exec.Command.Argv(), " ") != "/bin/sh -c c" {
		t.Fatalf("ServiceTasks = %+v, want the db service", services)
	}
}

func TestLoadProjectWithoutAFileIsAnEmptyProject(t *testing.T) {
	p, err := contract.LoadProject(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if p == nil || p.Version != 0 || len(p.Tasks) != 0 {
		t.Fatalf("project = %+v, want an empty project", p)
	}
}

func TestLoadProjectRejectsAnEmptyContractFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"pluto.yml": "# just a comment\n"})
	_, err := contract.LoadProject(dir)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err = %v, want an empty contract to be rejected", err)
	}
}

func TestTaskExecResolvesADeclaredTaskAsOneImplicitCommand(t *testing.T) {
	p, err := contract.ParseProject("version: 1\ntasks:\n  dev: { run: \"pnpm dev\" }\n  build: { run: [\"make\", \"test\"] }\n")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := p.TaskExec("dev")
	if err != nil {
		t.Fatalf("TaskExec(dev): %v", err)
	}
	if strings.Join(dev.Command.Argv(), " ") != "/bin/sh -c pnpm dev" {
		t.Fatalf("dev argv = %v, want the shell form", dev.Command.Argv())
	}
	build, err := p.TaskExec("build")
	if err != nil {
		t.Fatalf("TaskExec(build): %v", err)
	}
	if strings.Join(build.Command.Argv(), " ") != "make test" {
		t.Fatalf("build argv = %v, want the declared argv", build.Command.Argv())
	}
	if _, err := p.TaskExec("nope"); err == nil || !errors.Is(err, contract.ErrNoSuchTask) {
		t.Fatalf("TaskExec(nope) error = %v, want ErrNoSuchTask", err)
	}
}

func TestProjectHashChangesWithTheContract(t *testing.T) {
	base, err := contract.ParseProject("version: 1\ntasks:\n  dev: { run: a }\n")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := contract.ParseProject("version: 1\ntasks:\n  dev: { run: b }\n")
	if err != nil {
		t.Fatal(err)
	}
	if base.Hash() == edited.Hash() {
		t.Fatal("hash ignored a task command edit")
	}
}
