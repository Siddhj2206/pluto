package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestRunRoundTrip(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	in := Run{ID: "r_1", Repo: "o/r", Box: "b", RunnerID: 7, State: StateFailed, Reason: "x", GitHubRunID: 9,
		Workflow: "ci", Job: "j", Event: "push", Ref: "refs/heads/m", SHA: "s", Workspace: "/w", BoxKept: true,
		CreatedAt: now, StartedAt: now, FinishedAt: now, KeepUntil: now.Add(time.Hour)}
	if err := s.PutRun(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(ctx, "r_1")
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Fatalf("got %+v\nwant %+v", got, in)
	}
	if _, err := s.Run(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestLiveAndRunsFilter(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	for i, r := range []Run{
		{ID: "wait", State: StateWaiting}, {ID: "run", State: StateRunning},
		{ID: "kept", State: StateFailed, BoxKept: true}, {ID: "done", State: StatePassed},
		{ID: "expired", State: StateExpired},
	} {
		r.CreatedAt = at.Add(time.Duration(i) * time.Minute)
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.Live(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 3 {
		t.Fatalf("live = %+v", live)
	}
	runs, err := s.Runs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 || runs[0].ID != "done" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestPruneKeepsRecentActiveAndKeptRuns(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	put := func(r Run, minute int) {
		r.CreatedAt = at.Add(time.Duration(minute) * time.Minute)
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	put(Run{ID: "old-kept", State: StateFailed, BoxKept: true}, 0)
	put(Run{ID: "old-running", State: StateRunning}, 1)
	for i := range 5 {
		put(Run{ID: fmt.Sprintf("done-%d", i), State: StatePassed}, 2+i)
	}
	if err := s.Prune(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"old-kept": true, "old-running": true, "done-4": true, "done-3": true, "done-2": false, "done-0": false} {
		_, err := s.Run(ctx, id)
		if (err == nil) != want {
			t.Errorf("%s: exists = %v, want %v", id, err == nil, want)
		}
	}
}

func TestHostIDIsStable(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	calls := 0
	gen := func() string { calls++; return fmt.Sprintf("id%d", calls) }
	a, err := s.HostID(ctx, gen)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.HostID(ctx, gen)
	if err != nil || a != b || calls != 1 {
		t.Fatalf("%s %s %d %v", a, b, calls, err)
	}
}

func TestRepoLookupAndReplace(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, err := s.Repo(ctx, "o/r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	for _, tok := range []string{"one", "two"} {
		if err := s.PutRepo(ctx, Repo{Name: "o/r", Token: tok, ConnectedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Repo(ctx, "o/r")
	if err != nil || r.Token != "two" {
		t.Fatalf("%+v %v", r, err)
	}
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSecretsSetReplaceAndListNamesOnly(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	mustNil(t, s.SetSecret(ctx, "o/r", "API_TOKEN", "first-value"))
	mustNil(t, s.SetSecret(ctx, "o/r", "API_TOKEN", "second-value"))
	mustNil(t, s.SetSecret(ctx, "o/r", "DEPLOY_KEY", "key-value"))

	values, err := s.Secrets(ctx, "o/r")
	mustNil(t, err)
	if len(values) != 2 || values["API_TOKEN"] != "second-value" || values["DEPLOY_KEY"] != "key-value" {
		t.Fatalf("values = %v", values)
	}
	names, err := s.SecretNames(ctx, "o/r")
	mustNil(t, err)
	if got := strings.Join(names, ","); got != "API_TOKEN,DEPLOY_KEY" {
		t.Fatalf("names = %s", got)
	}
	listed := strings.Join(names, "\n")
	for _, v := range []string{"second-value", "key-value", "first-value"} {
		if strings.Contains(listed, v) {
			t.Errorf("SecretNames leaked a value: %q", v)
		}
	}
}

func TestSecretsAreIsolatedPerRepo(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	mustNil(t, s.SetSecret(ctx, "o/a", "TOKEN", "a-value"))
	mustNil(t, s.SetSecret(ctx, "o/b", "TOKEN", "b-value"))
	for repo, want := range map[string]string{"o/a": "a-value", "o/b": "b-value"} {
		values, err := s.Secrets(ctx, repo)
		mustNil(t, err)
		if values["TOKEN"] != want {
			t.Errorf("%s: TOKEN = %q, want %q", repo, values["TOKEN"], want)
		}
	}
	names, err := s.SecretNames(ctx, "o/none")
	mustNil(t, err)
	if len(names) != 0 {
		t.Errorf("o/none has secrets %v", names)
	}
	found, err := s.DeleteSecret(ctx, "o/a", "TOKEN")
	mustNil(t, err)
	if !found {
		t.Fatal("deleting an existing secret reported not found")
	}
	if values, _ := s.Secrets(ctx, "o/b"); values["TOKEN"] != "b-value" {
		t.Errorf("deleting o/a's secret removed o/b's: %v", values)
	}
}

func TestDeleteSecretReportsWhetherItExisted(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	mustNil(t, s.SetSecret(ctx, "o/r", "TOKEN", "v"))
	tests := []struct {
		name, repo, secret string
		want               bool
	}{
		{"existing", "o/r", "TOKEN", true},
		{"already deleted", "o/r", "TOKEN", false},
		{"never set", "o/r", "OTHER", false},
		{"set for another repo", "o/x", "TOKEN", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.DeleteSecret(ctx, tt.repo, tt.secret)
			mustNil(t, err)
			if got != tt.want {
				t.Fatalf("found = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGrantsArePerRepoAndRevocable(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	check := func(repo string, want bool) {
		t.Helper()
		got, err := s.Granted(ctx, repo, "privileged")
		mustNil(t, err)
		if got != want {
			t.Fatalf("Granted(%s) = %v, want %v", repo, got, want)
		}
	}
	check("o/r", false)
	mustNil(t, s.Grant(ctx, "o/r", "privileged"))
	mustNil(t, s.Grant(ctx, "o/r", "privileged")) // Granting twice is harmless.
	check("o/r", true)
	check("o/other", false)

	mustNil(t, s.Grant(ctx, "o/other", "privileged"))
	mustNil(t, s.Revoke(ctx, "o/r", "privileged"))
	mustNil(t, s.Revoke(ctx, "o/r", "privileged")) // Revoking twice is harmless.
	check("o/r", false)
	check("o/other", true)
}

func TestBoxesRoundTrip(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	in := Box{Name: "pluto-h-box-o-r", Repo: "o/r", Image: "img:1", Privileged: true,
		WorkspaceVolume: "pluto-h-box-o-r-workspace", HomeVolume: "pluto-h-box-o-r-home", CreatedAt: at,
		DataVolumes: map[string]string{"/var/lib/containers": "vol-a", "/data": "vol-b"}}
	other := Box{Name: "pluto-h-box-o-x", Repo: "o/x", Image: "img:2", WorkspaceVolume: "w", HomeVolume: "h", CreatedAt: at}
	mustNil(t, s.PutBox(ctx, in))
	mustNil(t, s.PutBox(ctx, other))

	got, err := s.BoxByName(ctx, in.Name)
	mustNil(t, err)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v\nwant %+v", got, in)
	}
	all, err := s.Boxes(ctx, "")
	mustNil(t, err)
	if len(all) != 2 {
		t.Fatalf("all boxes = %+v", all)
	}
	mine, err := s.Boxes(ctx, "o/r")
	mustNil(t, err)
	if len(mine) != 1 || !reflect.DeepEqual(mine[0], in) {
		t.Fatalf("o/r boxes = %+v", mine)
	}
	if _, err := s.BoxByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// Changing the volumes replaces the old set.
	in.DataVolumes = map[string]string{"/data": "vol-b"}
	mustNil(t, s.PutBox(ctx, in))
	if got, _ := s.BoxByName(ctx, in.Name); !reflect.DeepEqual(got.DataVolumes, in.DataVolumes) {
		t.Fatalf("volumes = %v", got.DataVolumes)
	}
	mustNil(t, s.DeleteBox(ctx, in.Name))
	var left int
	mustNil(t, s.db.QueryRow(`SELECT COUNT(*) FROM box_volumes WHERE box = ?`, in.Name).Scan(&left))
	if left != 0 {
		t.Fatalf("%d volume rows left after delete", left)
	}
	if _, err := s.BoxByName(ctx, in.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted box err = %v", err)
	}
}

func TestSourcesAndSchedulesRoundTrip(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	src := Source{Repo: "o/r", URL: "https://github.com/o/r.git", Branch: "main", SHA: "abc123",
		Config: `{"tasks":{}}`, Problem: "couldn't fetch", FetchedAt: at}
	mustNil(t, s.PutSource(ctx, src))
	sources, err := s.Sources(ctx)
	mustNil(t, err)
	if len(sources) != 1 || sources[0] != src {
		t.Fatalf("sources = %+v", sources)
	}

	sc := Schedule{Repo: "o/r", Task: "nightly", Expr: "0 3 * * *", CheckedAt: at}
	mustNil(t, s.PutSchedule(ctx, sc))
	schedules, err := s.Schedules(ctx)
	mustNil(t, err)
	if len(schedules) != 1 || schedules[0] != sc {
		t.Fatalf("schedules = %+v", schedules)
	}
	// Putting the same task again replaces its expression.
	sc.Expr = "0 4 * * *"
	mustNil(t, s.PutSchedule(ctx, sc))
	schedules, err = s.Schedules(ctx)
	mustNil(t, err)
	if len(schedules) != 1 || schedules[0].Expr != "0 4 * * *" {
		t.Fatalf("after replace: %+v", schedules)
	}
}

func TestDeleteSourceAlsoDeletesItsSchedules(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	mustNil(t, s.PutSource(ctx, Source{Repo: "o/r", Branch: "main", FetchedAt: at}))
	mustNil(t, s.PutSource(ctx, Source{Repo: "o/other", Branch: "main", FetchedAt: at}))
	mustNil(t, s.PutSchedule(ctx, Schedule{Repo: "o/r", Task: "nightly", Expr: "0 3 * * *", CheckedAt: at}))
	mustNil(t, s.PutSchedule(ctx, Schedule{Repo: "o/r", Task: "weekly", Expr: "0 3 * * 1", CheckedAt: at}))
	mustNil(t, s.PutSchedule(ctx, Schedule{Repo: "o/other", Task: "nightly", Expr: "0 3 * * *", CheckedAt: at}))

	mustNil(t, s.DeleteSource(ctx, "o/r"))
	sources, err := s.Sources(ctx)
	mustNil(t, err)
	if len(sources) != 1 || sources[0].Repo != "o/other" {
		t.Fatalf("sources = %+v", sources)
	}
	schedules, err := s.Schedules(ctx)
	mustNil(t, err)
	if len(schedules) != 1 || schedules[0].Repo != "o/other" {
		t.Fatalf("schedules = %+v", schedules)
	}
}

func TestTaskRunFieldsRoundTrip(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	in := Run{ID: "r_task", Repo: "o/r", Box: "b", State: StateRunning, Workspace: "/workspace",
		Ref: "main", SHA: "abc", Kind: KindTask, Job: "nightly", Trigger: TriggerSchedule,
		Note: "was due Jan 15 08:00, and the machine was asleep", Deadline: now.Add(time.Hour),
		Spec: `{"task":{"name":"nightly"}}`, Upload: "upload-1.tar",
		CreatedAt: now, StartedAt: now}
	mustNil(t, s.PutRun(ctx, in))
	got, err := s.Run(ctx, in.ID)
	mustNil(t, err)
	if got != in {
		t.Fatalf("got %+v\nwant %+v", got, in)
	}
}

func TestQueuedListsOldestFirstWithIDAsTieBreak(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	for _, r := range []Run{
		{ID: "c", State: StateQueued, CreatedAt: at.Add(2 * time.Minute)},
		{ID: "b", State: StateQueued, CreatedAt: at.Add(time.Minute)},
		{ID: "a", State: StateQueued, CreatedAt: at.Add(time.Minute)},
		{ID: "running", State: StateRunning, CreatedAt: at},
		{ID: "done", State: StatePassed, CreatedAt: at},
	} {
		mustNil(t, s.PutRun(ctx, r))
	}
	queued, err := s.Queued(ctx)
	mustNil(t, err)
	var ids []string
	for _, r := range queued {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "a,b,c" {
		t.Fatalf("queued = %s", got)
	}
}

func TestRunsSinceFiltersByTimeAndExpired(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	for _, r := range []Run{
		{ID: "old", State: StatePassed, CreatedAt: at.Add(-time.Minute)},
		{ID: "t0", State: StatePassed, CreatedAt: at},
		{ID: "t1", State: StateFailed, CreatedAt: at.Add(time.Minute)},
		{ID: "t2", State: StateSkipped, CreatedAt: at.Add(2 * time.Minute)},
		{ID: "gone", State: StateExpired, CreatedAt: at.Add(3 * time.Minute)},
	} {
		mustNil(t, s.PutRun(ctx, r))
	}
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{"everything since the cutoff, newest first", 10, "t2,t1"},
		{"limit applies after ordering", 1, "t2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs, err := s.RunsSince(ctx, at.Add(time.Minute-time.Second), tt.limit)
			mustNil(t, err)
			var ids []string
			for _, r := range runs {
				ids = append(ids, r.ID)
			}
			if got := strings.Join(ids, ","); got != tt.want {
				t.Fatalf("runs = %s, want %s", got, tt.want)
			}
		})
	}
}

// oldRunsTable is the runs table from before addedRunColumns existed.
const oldRunsTable = `CREATE TABLE runs (
	id TEXT PRIMARY KEY, repo TEXT NOT NULL, box TEXT NOT NULL, runner_id INTEGER NOT NULL,
	state TEXT NOT NULL, reason TEXT NOT NULL, github_run_id INTEGER NOT NULL,
	workflow TEXT NOT NULL, job TEXT NOT NULL, event TEXT NOT NULL, ref TEXT NOT NULL,
	sha TEXT NOT NULL, workspace TEXT NOT NULL, box_kept INTEGER NOT NULL,
	created_at INTEGER NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL,
	keep_until INTEGER NOT NULL
)`

func TestOpenAddsTheNewRunColumnsToAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	mustNil(t, err)
	_, err = db.Exec(oldRunsTable)
	mustNil(t, err)
	_, err = db.Exec(`INSERT INTO runs (id, repo, box, runner_id, state, reason, github_run_id, workflow, job, event,
		ref, sha, workspace, box_kept, created_at, started_at, finished_at, keep_until)
		VALUES ('old', 'o/r', 'b', 1, 'passed', '', 9, 'ci', 'build', 'push', 'refs/heads/m', 'abc', '/w', 0,
		1800000000, 0, 1800000100, 0)`)
	mustNil(t, err)
	mustNil(t, db.Close())

	s, err := Open(path)
	mustNil(t, err)
	t.Cleanup(func() { s.Close() })

	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('runs')`)
	mustNil(t, err)
	have := map[string]bool{}
	for rows.Next() {
		var name string
		mustNil(t, rows.Scan(&name))
		have[name] = true
	}
	mustNil(t, rows.Close())
	for _, col := range addedRunColumns {
		name, _, _ := strings.Cut(col, " ")
		if !have[name] {
			t.Errorf("column %s wasn't added", name)
		}
	}

	r, err := s.Run(context.Background(), "old")
	mustNil(t, err)
	if r.Job != "build" || r.Kind != "" || r.Trigger != "" || r.Note != "" || r.Spec != "" || r.Upload != "" || !r.Deadline.IsZero() {
		t.Fatalf("old run after migration = %+v", r)
	}

	// Opening an up-to-date database again changes nothing and fails nothing.
	mustNil(t, s.Close())
	s2, err := Open(path)
	mustNil(t, err)
	mustNil(t, s2.Close())
}

func TestOpenLowercasesRepoNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutRun(ctx, Run{ID: "r_1", Repo: "Own/Repo", Box: "b"}))
	must(s.PutRun(ctx, Run{ID: "r_2", Repo: "local/Dir-abc123", Box: "b2"}))
	must(s.PutBox(ctx, Box{Name: "pluto-box-own-repo-111111", Repo: "Own/Repo"}))
	must(s.Grant(ctx, "Own/Repo", "privileged"))
	must(s.Grant(ctx, "own/repo", "privileged")) // Already there under the other spelling.
	must(s.SetSecret(ctx, "Own/Repo", "K", "v"))
	must(s.PutRepo(ctx, Repo{Name: "Own/Repo", Token: "t"}))
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r, err := s.Run(ctx, "r_1"); err != nil || r.Repo != "own/repo" {
		t.Fatalf("run repo = %q, %v", r.Repo, err)
	}
	if r, _ := s.Run(ctx, "r_2"); r.Repo != "local/Dir-abc123" {
		t.Fatalf("local key changed: %q", r.Repo)
	}
	if b, err := s.Boxes(ctx, "own/repo"); err != nil || len(b) != 1 || b[0].Name != "pluto-box-own-repo-111111" {
		t.Fatalf("boxes = %+v, %v", b, err)
	}
	if v, _ := s.Secrets(ctx, "own/repo"); v["K"] != "v" {
		t.Fatalf("secrets = %v", v)
	}
	if r, err := s.Repo(ctx, "own/repo"); err != nil || r.Token != "t" {
		t.Fatalf("repo = %+v, %v", r, err)
	}
	if _, err := s.Repo(ctx, "Own/Repo"); err != nil {
		t.Fatalf("lookup by mixed case: %v", err)
	}
}

func TestOpenResolvesTwinSpellingsAndLeavesNonGitHubKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO repos VALUES ('Foo/Bar', 'old', 1), ('foo/bar', 'new', 2), ('Solo/Repo', 'solo', 3)`,
		`INSERT INTO secrets VALUES ('Foo/Bar', 'A', 'mixed'), ('foo/bar', 'A', 'lower'), ('Foo/Bar', 'B', 'only-mixed')`,
		`INSERT INTO grants VALUES ('Foo/Bar', 'privileged'), ('foo/bar', 'privileged')`,
		`INSERT INTO sources VALUES ('Foo/Bar', 'u', 'main', 's', '', '', 1), ('foo/bar', 'u', 'main', 's', '', '', 2)`,
		`INSERT INTO schedules VALUES ('Foo/Bar', 't', 'x', 1), ('foo/bar', 't', 'x', 2)`,
		`INSERT INTO repos VALUES ('gitlab.com/Own/Repo', 'g', 1), ('srv/Repos/Foo', 'p', 1), ('Dir/Foo', 'd', 1), ('dir/foo', 'd2', 1)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	for range 2 { // Twice: the migration is idempotent.
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		count := func(q string) (n int) {
			t.Helper()
			if err := s.db.QueryRow(q).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		for _, tb := range []string{"secrets WHERE name = 'A'", "grants", "sources", "schedules"} {
			if n := count(`SELECT count(*) FROM ` + tb); n != 1 {
				t.Errorf("%s: %d rows", tb, n)
			}
		}
		if n := count(`SELECT count(*) FROM secrets WHERE repo != lower(repo) OR repo != 'foo/bar'`); n != 0 {
			t.Errorf("secrets not lowercased: %d", n)
		}
		if r, err := s.Repo(ctx, "Foo/Bar"); err != nil || r.Token != "new" {
			t.Errorf("repo = %+v, %v", r, err)
		}
		if n := count(`SELECT count(*) FROM repos WHERE name IN ('Foo/Bar', 'Solo/Repo')`); n != 0 {
			t.Errorf("mixed-case repos left: %d", n)
		}
		if n := count(`SELECT count(*) FROM repos WHERE name IN ('solo/repo', 'gitlab.com/Own/Repo', 'srv/Repos/Foo')`); n != 3 {
			t.Errorf("repos wrong: %d", n)
		}
		if n := count(`SELECT count(*) FROM schedules WHERE repo = 'foo/bar'`); n != 1 {
			t.Errorf("schedule missing")
		}
		s.Close()
	}
}
