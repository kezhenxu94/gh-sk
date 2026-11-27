package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestGopass opens a throwaway gopass store (git-backed) encrypted to a
// throwaway gpg key. It needs gpg; without it the test is skipped, unless
// GHSK_GOPASS_TESTS=1 (as in CI) makes that a failure.
func newTestGopass(t *testing.T) (Gopass, string) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		if os.Getenv("GHSK_GOPASS_TESTS") == "1" {
			t.Fatal("gpg not found")
		}
		t.Skip("gpg not found")
	}
	// A short GNUPGHOME keeps the gpg-agent socket path under the OS limit.
	gnupg, err := os.MkdirTemp("/tmp", "ghsk-gpg")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GNUPGHOME", gnupg)
	t.Cleanup(func() {
		_ = exec.Command("gpgconf", "--kill", "gpg-agent").Run()
		_ = os.RemoveAll(gnupg)
	})
	// Keep gopass away from the real config and stores.
	t.Setenv("GOPASS_HOMEDIR", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PASSWORD_STORE_DIR", dir)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "gh-sk-test")
	}
	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("gpg", "--batch", "--passphrase", "", "--quick-gen-key", "gh-sk-test", "default", "default", "never")
	var fpr string
	for _, line := range strings.Split(run("gpg", "--with-colons", "--list-secret-keys", "gh-sk-test"), "\n") {
		if f := strings.Split(line, ":"); f[0] == "fpr" {
			fpr = f[9]
			break
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gpg-id"), []byte(fpr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("git", "init", "-q")
	run("git", "add", ".gpg-id")
	run("git", "commit", "-q", "-m", "init")

	g, err := NewGopass()
	if err != nil {
		t.Fatal(err)
	}
	return g, dir
}

func TestGopass(t *testing.T) {
	g, dir := newTestGopass(t)
	testStore(t, g, "github.com/o/r/env/prod")

	// Values round-trip exactly, which raw gopass secrets would not.
	for _, v := range []string{"no newline", "trailing\n", "crlf\r\nline\r\n", "", "kind: github-variable"} {
		if err := g.Set(Item{Kind: Secret, Scope: "github.com/o", Name: "X", Value: v}); err != nil {
			t.Fatal(err)
		}
		if it, err := g.Get(Secret, "github.com/o", "X"); err != nil || it.Value != v {
			t.Errorf("Get(%q) = %q, %v", v, it.Value, err)
		}
	}

	// Modified is stored in the entry: file times are ignored, and SetMeta keeps it.
	before, _ := g.Get(Secret, "github.com/o", "X")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "gh-sk/github.com/o/X.gpg"), future, future); err != nil {
		t.Fatal(err)
	}
	if err := g.SetMeta(Secret, "github.com/o", "X", Meta{SyncedAt: future}); err != nil {
		t.Fatal(err)
	}
	if after, _ := g.Get(Secret, "github.com/o", "X"); before.Modified.IsZero() || !after.Modified.Equal(before.Modified) {
		t.Errorf("Modified %v -> %v", before.Modified, after.Modified)
	}

	// Every write is committed, so `gopass sync` / git can carry it.
	if out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err != nil || len(out) != 0 {
		t.Errorf("uncommitted changes: %q, %v", out, err)
	}

	// SetAll commits (and so pushes) once for all items.
	commits := func() int {
		out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").Output()
		n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	n := commits()
	if err := SetAll(g, []Item{{Kind: Secret, Scope: "github.com/b", Name: "A", Value: "a"}, {Kind: Variable, Scope: "github.com/b", Name: "B", Value: "b"}}); err != nil {
		t.Fatal(err)
	}
	if got := commits() - n; got != 1 {
		t.Errorf("SetAll made %d commits, want 1", got)
	}
	if out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); len(out) != 0 {
		t.Errorf("SetAll left uncommitted changes: %q", out)
	}
	if it, err := g.Get(Variable, "github.com/b", "B"); err != nil || it.Value != "b" {
		t.Errorf("B = %q, %v", it.Value, err)
	}

	if err := g.Set(Item{Kind: Secret, Scope: "github.com/../..", Name: "X", Value: "v"}); err == nil {
		t.Error("Set outside the gh-sk prefix should fail")
	}
}
