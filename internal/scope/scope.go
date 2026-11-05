// Package scope parses GitHub-like scope paths such as
// github.com/org, github.com/org/repo and github.com/org/repo/env/name.
package scope

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cli/go-gh/v2/pkg/repository"
)

const host = "github.com"

type Scope struct {
	Org  string
	Repo string
	Env  string
}

func (s Scope) IsOrg() bool  { return s.Repo == "" }
func (s Scope) IsEnv() bool  { return s.Env != "" }
func (s Scope) IsRepo() bool { return s.Repo != "" && s.Env == "" }

func (s Scope) String() string {
	p := host + "/" + s.Org
	if s.Repo != "" {
		p += "/" + s.Repo
	}
	if s.Env != "" {
		p += "/env/" + s.Env
	}
	return p
}

// Parse accepts the forms listed in the package doc, with or without the github.com/ prefix.
func Parse(s string) (Scope, error) {
	p := strings.Trim(strings.TrimPrefix(strings.TrimSpace(s), host+"/"), "/")
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if part == "" {
			return Scope{}, fmt.Errorf("invalid scope %q", s)
		}
	}
	switch {
	case len(parts) == 1:
		return Scope{Org: parts[0]}, nil
	case len(parts) == 2:
		return Scope{Org: parts[0], Repo: parts[1]}, nil
	case len(parts) == 4 && parts[2] == "env":
		return Scope{Org: parts[0], Repo: parts[1], Env: parts[3]}, nil
	}
	return Scope{}, fmt.Errorf("invalid scope %q: want github.com/<org>[/<repo>[/env/<environment>]]", s)
}

// Current resolves the repository like gh does: GH_REPO, `gh repo set-default`,
// then the git remotes of the current directory.
func Current() (Scope, error) {
	if os.Getenv("GH_REPO") == "" && exec.Command("git", "rev-parse", "--is-inside-work-tree").Run() != nil {
		return Scope{}, errors.New("no scope given and not inside a git repository; pass a scope such as my-org/my-repo (or --all where supported)")
	}
	r, err := repository.Current()
	if err != nil {
		return Scope{}, fmt.Errorf("no scope given: %w", err)
	}
	if r.Host != host {
		return Scope{}, fmt.Errorf("current repository is on %s; only %s is supported", r.Host, host)
	}
	return Scope{Org: r.Owner, Repo: r.Name}, nil
}
