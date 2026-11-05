// Package cli implements the ghsk CLI.
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kezhenxu94/github-secrets-keychain/internal/scope"
	"github.com/kezhenxu94/github-secrets-keychain/internal/store"
)

var st store.Store = store.Keychain{}

var root = &cobra.Command{
	Use:   "ghsk",
	Short: "Keep GitHub Actions secrets and variables in the macOS Keychain and sync them to GitHub",
	Long: `ghsk stores GitHub Actions secrets and variables in the macOS Keychain under
GitHub-like scopes and syncs them to GitHub.

Scopes:
  github.com/<org>                          organization
  github.com/<org>/<repo>                   repository
  github.com/<org>/<repo>/env/<environment> environment
The github.com/ prefix is optional. When the scope is omitted, it is derived
from the origin remote of the current git repository.`,
	SilenceUsage: true,
}

func Execute() {
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func kindOf(isVar bool) store.Kind {
	if isVar {
		return store.Variable
	}
	return store.Secret
}

// resolveScope parses s, or falls back to the git origin remote when empty.
func resolveScope(s string) (scope.Scope, error) {
	if s == "" {
		return scope.FromGitRemote()
	}
	return scope.Parse(s)
}

// scopeAndName handles the "[scope] NAME" argument form.
func scopeAndName(args []string) (scope.Scope, string, error) {
	sArg, name := "", args[len(args)-1]
	if len(args) == 2 {
		sArg = args[0]
	}
	sc, err := resolveScope(sArg)
	if err != nil {
		return sc, "", err
	}
	name, err = store.NormalizeName(name)
	return sc, name, err
}

// readValue prompts without echo on a terminal, or reads stdin (minus one trailing newline).
func readValue(name string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprintf(os.Stderr, "Value for %s: ", name)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}

// parseDotenv reads KEY=VALUE lines, skipping blanks and comments and
// stripping an optional "export " prefix and matching surrounding quotes.
func parseDotenv(r io.Reader) (map[string]string, error) {
	vals := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '='", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		vals[strings.TrimSpace(k)] = v
	}
	return vals, sc.Err()
}
