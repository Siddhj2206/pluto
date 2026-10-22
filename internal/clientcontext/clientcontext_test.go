package clientcontext_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/clientcontext"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
}

// checkout makes a committed repository on main with the given remotes.
func checkout(t *testing.T, remotes map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	git(t, dir, "add", "README.md")
	git(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	for name, url := range remotes {
		git(t, dir, "remote", "add", name, url)
	}
	return dir
}

func TestDetectPrefersOrigin(t *testing.T) {
	dir := checkout(t, map[string]string{
		"upstream": "https://example.com/upstream.git",
		"origin":   "https://example.com/cipher.git",
	})
	ctx, err := clientcontext.Detect(dir)
	if err != nil || ctx == nil {
		t.Fatalf("Detect = %v, %v", ctx, err)
	}
	if ctx.Remote != "origin" || ctx.Project != "cipher" || ctx.Source != "https://example.com/cipher.git" {
		t.Fatalf("ctx = %+v, want origin/cipher", ctx)
	}
	if ctx.Branch != "main" || ctx.Head == "" || ctx.Dirty || ctx.NoRemote {
		t.Fatalf("ctx = %+v, want a clean main checkout with a HEAD", ctx)
	}
}

func TestDetectUsesTheTrackedRemoteWithoutOrigin(t *testing.T) {
	dir := checkout(t, map[string]string{
		"fork":   "https://example.com/fork.git",
		"mirror": "https://example.com/mirror.git",
	})
	git(t, dir, "config", "branch.main.remote", "mirror")

	ctx, err := clientcontext.Detect(dir)
	if err != nil || ctx == nil {
		t.Fatalf("Detect = %v, %v", ctx, err)
	}
	if ctx.Remote != "mirror" || ctx.Project != "mirror" {
		t.Fatalf("ctx = %+v, want the branch's tracked remote", ctx)
	}
}

func TestDetectUsesASingleRemote(t *testing.T) {
	dir := checkout(t, map[string]string{"upstream": "git@example.com:team/app.git"})
	ctx, err := clientcontext.Detect(dir)
	if err != nil || ctx == nil {
		t.Fatalf("Detect = %v, %v", ctx, err)
	}
	if ctx.Remote != "upstream" || ctx.Project != "app" {
		t.Fatalf("ctx = %+v, want the only remote and its project", ctx)
	}
}

// Several remotes with no origin and no tracking remote are ambiguous: the
// client refuses to guess which repository the box should clone.
func TestDetectReportsAmbiguousRemotesAsNoRemote(t *testing.T) {
	dir := checkout(t, map[string]string{
		"one": "https://example.com/one.git",
		"two": "https://example.com/two.git",
	})
	ctx, err := clientcontext.Detect(dir)
	if err != nil || ctx == nil {
		t.Fatalf("Detect = %v, %v", ctx, err)
	}
	if !ctx.NoRemote || ctx.Project != "" || ctx.Source != "" || ctx.Branch != "main" {
		t.Fatalf("ctx = %+v, want a no-remote context that keeps the branch", ctx)
	}
}

// A remote URL with embedded credentials loses them before it reaches the host
// command line.
func TestDetectStripsRemoteCredentials(t *testing.T) {
	dir := checkout(t, map[string]string{"origin": "https://user:secret@example.com/cipher.git"})
	ctx, err := clientcontext.Detect(dir)
	if err != nil || ctx == nil {
		t.Fatalf("Detect = %v, %v", ctx, err)
	}
	if ctx.Source != "https://example.com/cipher.git" {
		t.Fatalf("source = %q, want the credential-free URL", ctx.Source)
	}
}

func TestDetectOutsideACheckoutIsNoContext(t *testing.T) {
	ctx, err := clientcontext.Detect(t.TempDir())
	if err != nil || ctx != nil {
		t.Fatalf("Detect = %v, %v, want no context", ctx, err)
	}
}

func TestProjectName(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"https://github.com/example/cipher.git", "cipher"},
		{"git@github.com:example/cipher.git", "cipher"},
		{"/srv/git/app/", "app"},
		{"https://example.com/example/cipher", "cipher"},
	} {
		if got := clientcontext.ProjectName(tc.source); got != tc.want {
			t.Fatalf("ProjectName(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestParseRejectsAMalformedContext(t *testing.T) {
	for _, value := range []string{`{"branch":"main"}`, `{`, `{"no_remote":true}`} {
		if _, err := clientcontext.Parse(value); err == nil {
			t.Fatalf("Parse(%q) succeeded, want an error", value)
		}
	}
	if ctx, err := clientcontext.Parse(""); err != nil || ctx != nil {
		t.Fatalf("Parse(\"\") = %v, %v, want no context", ctx, err)
	}
}
