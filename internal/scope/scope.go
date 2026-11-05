// Package scope parses GitHub-like scope paths such as
// github.com/org, github.com/org/repo and github.com/org/repo/env/name.
package scope

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
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

var remoteRe = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ParseRemoteURL extracts org/repo from a GitHub git remote URL (ssh or https).
func ParseRemoteURL(url string) (Scope, error) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return Scope{}, fmt.Errorf("remote %q is not a github.com repository", url)
	}
	return Scope{Org: m[1], Repo: m[2]}, nil
}

// FromGitRemote derives the repo scope from the origin remote of the current directory.
func FromGitRemote() (Scope, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return Scope{}, fmt.Errorf("no scope given and cannot read git origin remote: %w", err)
	}
	return ParseRemoteURL(string(out))
}
