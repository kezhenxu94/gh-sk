package scope

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	ok := map[string]Scope{
		"github.com/o":            {Org: "o"},
		"o":                       {Org: "o"},
		"github.com/o/r":          {Org: "o", Repo: "r"},
		"o/r/":                    {Org: "o", Repo: "r"},
		"github.com/o/r/env/prod": {Org: "o", Repo: "r", Env: "prod"},
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "github.com/", "o//r", "o/r/x", "o/r/env", "o/r/env/p/x"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) should fail", in)
		}
	}
	if s := (Scope{Org: "o", Repo: "r", Env: "p"}).String(); s != "github.com/o/r/env/p" {
		t.Errorf("String() = %q", s)
	}
}

func TestCurrent(t *testing.T) {
	t.Setenv("GH_REPO", "o/r")
	if got, err := Current(); err != nil || got != (Scope{Org: "o", Repo: "r"}) {
		t.Errorf("Current() with GH_REPO = %+v, %v", got, err)
	}

	t.Setenv("GH_REPO", "ghe.example.com/o/r")
	if _, err := Current(); err == nil || !strings.Contains(err.Error(), "only github.com") {
		t.Errorf("non-github.com host error = %v", err)
	}

	t.Setenv("GH_REPO", "")
	t.Chdir(t.TempDir())
	if _, err := Current(); err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Errorf("outside git error = %v", err)
	}
}

func TestCurrentFromGitRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GH_REPO", "")
	dir := t.TempDir()
	t.Chdir(dir)
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "git@github.com:my-org/my-repo.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if got, err := Current(); err != nil || got != (Scope{Org: "my-org", Repo: "my-repo"}) {
		t.Errorf("Current() from origin = %+v, %v", got, err)
	}
}
