package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parseFiles(files map[string]string) (*Config, error) {
	m := map[string][]byte{}
	for name, body := range files {
		m[name] = []byte(body)
	}
	return Parse(m)
}

func TestParseReadsTasksFromPlutoYMLAndTasksDir(t *testing.T) {
	cfg, err := parseFiles(map[string]string{
		"pluto.yml":         "image: ghcr.io/example/box:1\ntasks:\n  build:\n    run:\n      - make\n",
		"tasks/nightly.yml": "run:\n  - make test\nschedule: \"0 3 * * *\"\ntimeout: 2h\nsecrets:\n  - API_TOKEN\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "ghcr.io/example/box:1" {
		t.Errorf("image = %q", cfg.Image)
	}
	if len(cfg.Tasks) != 2 {
		t.Fatalf("tasks = %+v", cfg.Tasks)
	}
	build := cfg.Tasks["build"]
	if build.Name != "build" || len(build.Run) != 1 || build.Timeout != DefaultTimeout {
		t.Errorf("build = %+v", build)
	}
	nightly := cfg.Tasks["nightly"]
	if nightly.Name != "nightly" || nightly.Schedule != "0 3 * * *" || nightly.Timeout != 2*time.Hour {
		t.Errorf("nightly = %+v", nightly)
	}
	if len(nightly.Secrets) != 1 || nightly.Secrets[0] != "API_TOKEN" {
		t.Errorf("nightly secrets = %v", nightly.Secrets)
	}
}

func TestParseRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{"include key", map[string]string{"pluto.yml": "include: other.yml\n"}, "include"},
		{"jobs key", map[string]string{"pluto.yml": "jobs:\n  build:\n    run: [make]\n"}, "jobs"},
		{"unknown key in a task file", map[string]string{"tasks/x.yml": "run: [make]\nretries: 3\n"}, "retries"},
		{"no run in a task file", map[string]string{"tasks/x.yml": "schedule: \"0 3 * * *\"\n"}, "nothing to run"},
		{"empty run in pluto.yml", map[string]string{"pluto.yml": "tasks:\n  x:\n    run: []\n"}, "nothing to run"},
		{"dot in file name", map[string]string{"tasks/a.b.yml": "run: [make]\n"}, "isn't a valid task name"},
		{"hidden file name", map[string]string{"tasks/.hidden.yml": "run: [make]\n"}, "isn't a valid task name"},
		{"space in task name", map[string]string{"pluto.yml": "tasks:\n  bad name:\n    run: [make]\n"}, "isn't a valid task name"},
		{"unparseable schedule", map[string]string{"tasks/x.yml": "run: [make]\nschedule: every day\n"}, "schedule"},
		{"schedule with too few fields", map[string]string{"tasks/x.yml": "run: [make]\nschedule: \"0 3 * *\"\n"}, "schedule"},
		{"TZ in schedule", map[string]string{"tasks/x.yml": "run: [make]\nschedule: \"TZ=UTC 0 3 * * *\"\n"}, "local time"},
		{"unreadable timeout", map[string]string{"tasks/x.yml": "run: [make]\ntimeout: soon\n"}, "timeout"},
		{"zero timeout", map[string]string{"tasks/x.yml": "run: [make]\ntimeout: 0s\n"}, "timeout"},
		{"negative timeout", map[string]string{"tasks/x.yml": "run: [make]\ntimeout: -1h\n"}, "timeout"},
		{"lowercase secret name", map[string]string{"tasks/x.yml": "run: [make]\nsecrets: [api_token]\n"}, "should look like MY_SECRET"},
		{"dash in secret name", map[string]string{"tasks/x.yml": "run: [make]\nsecrets: [MY-SECRET]\n"}, "should look like MY_SECRET"},
		{"unknown requirement", map[string]string{"pluto.yml": "requires: [root]\n"}, "unknown requirement"},
		{"duplicate task across pluto.yml and tasks", map[string]string{
			"pluto.yml":       "tasks:\n  build:\n    run: [make]\n",
			"tasks/build.yml": "run: [make]\n",
		}, ".pluto/pluto.yml and .pluto/tasks/build.yml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseFiles(tt.files)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseAcceptsValidRequirementsAndTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		timeout time.Duration
		reqs    []string
	}{
		{"privileged requirement", map[string]string{"pluto.yml": "requires: [privileged]\ntasks:\n  build:\n    run: [make]\n"}, DefaultTimeout, []string{"privileged"}},
		{"no timeout gets the default", map[string]string{"tasks/x.yml": "run: [make]\n"}, DefaultTimeout, nil},
		{"90 minutes", map[string]string{"tasks/x.yml": "run: [make]\ntimeout: 90m\n"}, 90 * time.Minute, nil},
		{"six hours", map[string]string{"tasks/x.yml": "run: [make]\ntimeout: 6h\n"}, 6 * time.Hour, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseFiles(tt.files)
			if err != nil {
				t.Fatal(err)
			}
			task, ok := cfg.Tasks["x"]
			if !ok {
				task, ok = cfg.Tasks["build"]
			}
			if !ok {
				t.Fatalf("no task in %+v", cfg.Tasks)
			}
			if task.Timeout != tt.timeout {
				t.Errorf("timeout = %v, want %v", task.Timeout, tt.timeout)
			}
			if strings.Join(cfg.Requires, ",") != strings.Join(tt.reqs, ",") {
				t.Errorf("requires = %v, want %v", cfg.Requires, tt.reqs)
			}
		})
	}
}

func TestParseIgnoresFilesOutsideTasks(t *testing.T) {
	cfg, err := parseFiles(map[string]string{
		"notes.yml":          "this: is: not: yaml: [",
		"tasks/readme.md":    "not yaml",
		"tasks/sub/deep.yml": "run: [make]\n",
	})
	if err != nil {
		t.Fatalf("files outside tasks/ should be ignored: %v", err)
	}
	if len(cfg.Tasks) != 0 {
		t.Fatalf("tasks = %+v", cfg.Tasks)
	}
}

func TestLoadDirWithoutAPlutoDirIsEmpty(t *testing.T) {
	cfg, err := LoadDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Tasks) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadDirReadsTheDirectory(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, Dir, "tasks"), 0o755))
	must(os.WriteFile(filepath.Join(root, Dir, "pluto.yml"), []byte("tasks:\n  build:\n    run: [make]\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, Dir, "tasks", "nightly.yml"), []byte("run: [make test]\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, Dir, "tasks", "notes.txt"), []byte("ignored"), 0o644))
	cfg, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tasks) != 2 || cfg.Tasks["build"].Name != "build" || cfg.Tasks["nightly"].Name != "nightly" {
		t.Fatalf("tasks = %+v", cfg.Tasks)
	}
}

func TestParseRejectsYamlExtension(t *testing.T) {
	for _, name := range []string{"pluto.yaml", "tasks/nightly.yaml"} {
		_, err := parseFiles(map[string]string{name: "run: [make]\n"})
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want one naming the file", name, err)
		}
	}
}

func TestParseScheduleRejectsADateThatNeverComes(t *testing.T) {
	if _, err := ParseSchedule("0 0 31 2 *"); err == nil {
		t.Fatal("February 31 should be refused, or the task would fire on every tick")
	}
}

func TestValidSecretNameKeepsPlutosOwnNames(t *testing.T) {
	if ValidSecretName("PLUTO_SHA") {
		t.Error("PLUTO_SHA is set by Pluto, so a secret can't use it")
	}
	if !ValidSecretName("MY_TOKEN") {
		t.Error("MY_TOKEN should be fine")
	}
}
