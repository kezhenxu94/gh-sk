package scope

import "testing"

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

func TestParseRemoteURL(t *testing.T) {
	want := Scope{Org: "o", Repo: "r.js"}
	for _, u := range []string{
		"git@github.com:o/r.js.git\n",
		"https://github.com/o/r.js",
		"https://github.com/o/r.js.git",
		"ssh://git@github.com/o/r.js.git",
	} {
		got, err := ParseRemoteURL(u)
		if err != nil || got != want {
			t.Errorf("ParseRemoteURL(%q) = %+v, %v", u, got, err)
		}
	}
	if _, err := ParseRemoteURL("https://gitlab.com/o/r"); err == nil {
		t.Error("expected error for non-github remote")
	}
}
