package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kezhenxu94/gh-sk/internal/remote"
	"github.com/kezhenxu94/gh-sk/internal/scope"
	"github.com/kezhenxu94/gh-sk/internal/store"
)

// memRemote is an in-memory GitHub. Each write bumps updated_at by a minute.
type memRemote struct {
	clock   time.Time
	secrets map[string]map[string]remote.Entry // scope -> name -> entry (Value kept for assertions)
	vars    map[string]map[string]remote.Entry
}

func newMemRemote() *memRemote {
	return &memRemote{
		clock:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		secrets: map[string]map[string]remote.Entry{},
		vars:    map[string]map[string]remote.Entry{},
	}
}

func (m *memRemote) put(set map[string]map[string]remote.Entry, s, name, value string) {
	if set[s] == nil {
		set[s] = map[string]remote.Entry{}
	}
	m.clock = m.clock.Add(time.Minute)
	set[s][name] = remote.Entry{Value: value, UpdatedAt: m.clock}
}

func (m *memRemote) ListSecrets(_ context.Context, s scope.Scope) (map[string]remote.Entry, error) {
	out := map[string]remote.Entry{}
	for n, e := range m.secrets[s.String()] {
		out[n] = remote.Entry{UpdatedAt: e.UpdatedAt} // GitHub never returns secret values
	}
	return out, nil
}

func (m *memRemote) PutSecret(_ context.Context, s scope.Scope, name, value string, _ store.Meta) error {
	m.put(m.secrets, s.String(), name, value)
	return nil
}

func (m *memRemote) DeleteSecret(_ context.Context, s scope.Scope, name string) error {
	if _, ok := m.secrets[s.String()][name]; !ok {
		return fmt.Errorf("secret %s not found", name)
	}
	delete(m.secrets[s.String()], name)
	return nil
}

func (m *memRemote) ListVars(_ context.Context, s scope.Scope) (map[string]remote.Entry, error) {
	out := map[string]remote.Entry{}
	for n, e := range m.vars[s.String()] {
		out[n] = e
	}
	return out, nil
}

func (m *memRemote) PutVar(_ context.Context, s scope.Scope, name, value string, exists bool, _ store.Meta) error {
	if _, ok := m.vars[s.String()][name]; ok != exists {
		return fmt.Errorf("variable %s: exists=%v but found=%v", name, exists, ok)
	}
	m.put(m.vars, s.String(), name, value)
	return nil
}

func (m *memRemote) DeleteVar(_ context.Context, s scope.Scope, name string) error {
	if _, ok := m.vars[s.String()][name]; !ok {
		return fmt.Errorf("variable %s not found", name)
	}
	delete(m.vars[s.String()], name)
	return nil
}

const repo = "github.com/o/r"

type harness struct {
	t      *testing.T
	st     *store.Memory
	rc     *memRemote
	stdout bytes.Buffer
	stderr bytes.Buffer
}

// setup swaps the package dependencies for in-memory fakes. The current
// repository is o/r via GH_REPO.
func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, st: store.NewMemory(), rc: newMemRemote()}
	oldSt, oldRemote, oldIn, oldOut, oldErr, oldExec := st, newRemote, stdin, stdout, stderr, execve
	t.Cleanup(func() {
		st, newRemote, stdin, stdout, stderr, execve = oldSt, oldRemote, oldIn, oldOut, oldErr, oldExec
	})
	st = h.st
	newRemote = func() (remote.Remote, error) { return h.rc, nil }
	stdout, stderr = &h.stdout, &h.stderr
	t.Setenv("GH_REPO", "o/r")
	return h
}

func (h *harness) run(args ...string) error {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(&h.stderr)
	root.SetErr(&h.stderr)
	return root.Execute()
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	if err := h.run(args...); err != nil {
		h.t.Fatalf("gh sk %s: %v\nstderr: %s", strings.Join(args, " "), err, h.stderr.String())
	}
	return h.stdout.String()
}

func (h *harness) value(kind store.Kind, scope, name string) string {
	h.t.Helper()
	it, err := h.st.Get(kind, scope, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return it.Value
}

// ops parses status/push output into "kind NAME" -> op.
func ops(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && strings.HasPrefix(line, "  ") {
			m[f[1]+" "+f[2]] = f[0]
		}
	}
	return m
}

func TestSetGetRm(t *testing.T) {
	h := setup(t)
	h.mustRun("set", "npm_token", "-b", "v1")
	if got := h.mustRun("get", "NPM_TOKEN"); got != "v1\n" {
		t.Errorf("get = %q", got)
	}
	h.mustRun("set", "github.com/o/r/env/prod", "REGION", "--var", "--body", "eu")
	if got := h.value(store.Variable, "github.com/o/r/env/prod", "REGION"); got != "eu" {
		t.Errorf("env var = %q", got)
	}
	if err := h.run("get", "REGION"); err == nil {
		t.Error("get of a variable without --var should fail")
	}
	if err := h.run("set", "NPM_TOKEN", "--var", "-b", "x"); err == nil {
		t.Error("a variable may not shadow a secret in the same scope")
	}

	h.rc.put(h.rc.secrets, repo, "NPM_TOKEN", "v1")
	h.mustRun("rm", "NPM_TOKEN", "--remote")
	if _, ok := h.rc.secrets[repo]["NPM_TOKEN"]; ok {
		t.Error("rm --remote left the secret on GitHub")
	}
	if err := h.run("get", "NPM_TOKEN"); err == nil {
		t.Error("secret still in keychain after rm")
	}
	if err := h.run("rm", "NPM_TOKEN"); err == nil {
		t.Error("rm of a missing item should fail")
	}
}

func TestSetValidation(t *testing.T) {
	h := setup(t)
	for _, args := range [][]string{
		{"set", "X", "-b", ""},
		{"set", "1BAD", "-b", "v"},
		{"set", "GITHUB_X", "-b", "v"},
		{"set", "X", "-b", "v", "--visibility", "all"}, // repo scope
		{"set", "o", "X", "-b", "v", "--visibility", "selected"},
		{"set", "o", "X", "-b", "v", "--visibility", "all", "--repos", "a"},
		{"set", "o", "X", "-b", "v", "--visibility", "public"},
		{"set", "a/b/c", "X", "-b", "v"},
	} {
		if err := h.run(args...); err == nil {
			t.Errorf("gh sk %v should fail", args)
		}
	}
	if items, _ := h.st.List(store.Secret); len(items) != 0 {
		t.Fatalf("invalid sets stored %+v", items)
	}

	h.mustRun("set", "o", "ORG", "-b", "v", "--visibility", "selected", "--repos", "a,b")
	it, _ := h.st.Get(store.Secret, "github.com/o", "ORG")
	if it.Meta.Visibility != "selected" || !slices.Equal(it.Meta.Repos, []string{"a", "b"}) {
		t.Errorf("org meta = %+v", it.Meta)
	}
	h.mustRun("set", "o", "ORG", "-b", "v2")
	if it, _ = h.st.Get(store.Secret, "github.com/o", "ORG"); it.Meta.Visibility != "selected" {
		t.Errorf("re-set lost visibility: %+v", it.Meta)
	}
}

func TestSetFromStdin(t *testing.T) {
	h := setup(t)
	pipe := func(s string) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(s)
		_ = w.Close()
		stdin = r
	}
	pipe("line1\nline2\n")
	h.mustRun("set", "PEM")
	if got := h.value(store.Secret, repo, "PEM"); got != "line1\nline2" {
		t.Errorf("stdin value = %q (one trailing newline must be trimmed)", got)
	}
	pipe("")
	if err := h.run("set", "EMPTY"); err == nil {
		t.Error("empty stdin must be rejected")
	}
}

func TestLs(t *testing.T) {
	h := setup(t)
	for _, s := range []string{"o/r", "o/r/env/prod", "o/other", "o"} {
		h.mustRun("set", s, "TOKEN", "-b", "secret-value")
	}
	out := h.mustRun("ls")
	if !strings.Contains(out, "github.com/o/r\n") || !strings.Contains(out, "github.com/o/r/env/prod\n") ||
		strings.Contains(out, "github.com/o/other") || strings.Contains(out, "github.com/o\n") {
		t.Errorf("ls (current repo) =\n%s", out)
	}
	if strings.Contains(out, "secret-value") {
		t.Error("ls printed a value")
	}
	if out := h.mustRun("ls", "--all"); strings.Count(out, "TOKEN") != 4 {
		t.Errorf("ls --all =\n%s", out)
	}
	if out := h.mustRun("ls", "o"); strings.Count(out, "TOKEN") != 4 {
		t.Errorf("ls o =\n%s", out)
	}
	if err := h.run("ls", "--all", "o"); err == nil {
		t.Error("ls --all with a scope should fail")
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImport(t *testing.T) {
	h := setup(t)
	f := writeFile(t, `# comment
   # indented comment

export EXPORTED=1
lower_case = spaced value
SINGLE='it''s # not a comment'
DOUBLE="a=b"
MISMATCH="x'
EMPTY=
EMPTY_QUOTED=""
`)
	h.mustRun("import", "-f", f)
	want := map[string]string{
		"EXPORTED":   "1",
		"LOWER_CASE": "spaced value",
		"SINGLE":     "it''s # not a comment",
		"DOUBLE":     "a=b",
		"MISMATCH":   `"x'`,
	}
	for n, v := range want {
		if got := h.value(store.Secret, repo, n); got != v {
			t.Errorf("%s = %q, want %q", n, got, v)
		}
	}
	for _, n := range []string{"EMPTY", "EMPTY_QUOTED"} {
		if _, err := h.st.Get(store.Secret, repo, n); err == nil {
			t.Errorf("empty %s must be skipped", n)
		}
	}
	if !strings.Contains(h.stderr.String(), "skipped secret github.com/o/r/EMPTY: empty value") {
		t.Errorf("stderr = %s", h.stderr.String())
	}

	h.mustRun("import", "o/vars", "-f", writeFile(t, "REGION=eu\n"), "--var")
	if got := h.value(store.Variable, "github.com/o/vars", "REGION"); got != "eu" {
		t.Errorf("imported variable = %q", got)
	}

	// Invalid input aborts before anything is saved.
	for _, bad := range []string{"GOOD=1\nNO_EQUALS\n", "GOOD=1\n1BAD=2\n"} {
		if err := h.run("import", "o/bad", "-f", writeFile(t, bad)); err == nil {
			t.Errorf("import of %q should fail", bad)
		}
	}
	if out := h.mustRun("ls", "o/bad"); strings.Contains(out, "GOOD") {
		t.Error("failed import stored items")
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	h := setup(t)
	values := map[string]string{
		"PLAIN":  "abc",
		"SPACES": "  padded  ",
		"QUOTES": `it's "quoted"`,
		"HASH":   "a # b",
		"EQUALS": "k=v=w",
		"UNI":    "pässwörd ✓",
	}
	for n, v := range values {
		if err := h.st.Set(store.Item{Kind: store.Secret, Scope: repo, Name: n, Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	_ = h.st.Set(store.Item{Kind: store.Secret, Scope: repo, Name: "PEM", Value: "line1\nline2"})

	file := filepath.Join(t.TempDir(), "out.env")
	h.mustRun("export", "-f", file)
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("export file mode = %v, %v", info.Mode().Perm(), err)
	}
	content, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(content), "# secrets of github.com/o/r exported by gh sk") || strings.Contains(string(content), "PEM") {
		t.Errorf("export file =\n%s", content)
	}
	if !strings.Contains(h.stderr.String(), "skipped secret github.com/o/r/PEM: multi-line") {
		t.Errorf("stderr = %s", h.stderr.String())
	}

	h.mustRun("import", "o/copy", "-f", file)
	for n, v := range values {
		if got := h.value(store.Secret, "github.com/o/copy", n); got != v {
			t.Errorf("round trip %s = %q, want %q", n, got, v)
		}
	}

	if err := h.run("export", "-f", file); err == nil {
		t.Error("export must not overwrite an existing file")
	}
	empty := filepath.Join(t.TempDir(), "empty.env")
	if err := h.run("export", "o/none", "-f", empty); err == nil {
		t.Error("export of an empty scope should fail")
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Error("export of an empty scope created a file")
	}

	_ = h.st.Set(store.Item{Kind: store.Variable, Scope: repo, Name: "REGION", Value: "eu"})
	vars := filepath.Join(t.TempDir(), "vars.env")
	h.mustRun("export", "-f", vars, "--var")
	if content, _ := os.ReadFile(vars); !strings.Contains(string(content), "REGION='eu'\n") || strings.Contains(string(content), "PLAIN") {
		t.Errorf("variables export =\n%s", content)
	}
}

func TestExec(t *testing.T) {
	h := setup(t)
	var gotPath string
	var gotArgv, gotEnv []string
	execve = func(path string, argv, env []string) error {
		gotPath, gotArgv, gotEnv = path, argv, env
		return nil
	}
	t.Setenv("NPM_TOKEN", "from-shell")
	t.Setenv("KEEP_ME", "kept")
	h.mustRun("set", "NPM_TOKEN", "-b", "from-keychain")
	h.mustRun("set", "REGION", "--var", "-b", "eu")
	h.mustRun("set", "o/r/env/prod", "DEPLOY", "-b", "prod-only")

	h.mustRun("exec", "--", "sh", "-c", "echo hi", "--not-a-flag")
	if filepath.Base(gotPath) != "sh" || !slices.Equal(gotArgv, []string{"sh", "-c", "echo hi", "--not-a-flag"}) {
		t.Errorf("exec path=%q argv=%q", gotPath, gotArgv)
	}
	envOf := func(k string) []string {
		var vs []string
		for _, kv := range gotEnv {
			if name, v, _ := strings.Cut(kv, "="); name == k {
				vs = append(vs, v)
			}
		}
		return vs
	}
	if v := envOf("NPM_TOKEN"); !slices.Equal(v, []string{"from-keychain"}) {
		t.Errorf("NPM_TOKEN = %q, want keychain value exactly once", v)
	}
	if v := envOf("REGION"); !slices.Equal(v, []string{"eu"}) {
		t.Errorf("REGION = %q", v)
	}
	if v := envOf("KEEP_ME"); !slices.Equal(v, []string{"kept"}) {
		t.Errorf("existing environment not kept: %q", v)
	}
	if v := envOf("DEPLOY"); v != nil {
		t.Errorf("environment-scope secret leaked into repo exec: %q", v)
	}

	h.mustRun("exec", "o/r/env/prod", "--", "true")
	if v := envOf("DEPLOY"); !slices.Equal(v, []string{"prod-only"}) {
		t.Errorf("scope argument ignored: DEPLOY = %q", v)
	}

	for _, args := range [][]string{
		{"exec"},
		{"exec", "--"},
		{"exec", "sh"},
		{"exec", "o/r", "extra", "--", "sh"},
		{"exec", "--", "definitely-not-a-command-gh-sk"},
	} {
		if err := h.run(args...); err == nil {
			t.Errorf("gh sk %v should fail", args)
		}
	}
}

func TestPushLifecycle(t *testing.T) {
	h := setup(t)
	h.mustRun("set", "A", "-b", "a1")
	h.mustRun("set", "V", "--var", "-b", "v1")

	if got := ops(h.mustRun("status")); got["secret A"] != "create" || got["var V"] != "create" {
		t.Fatalf("initial status = %v", got)
	}
	h.mustRun("push", "--dry-run")
	if len(h.rc.secrets[repo])+len(h.rc.vars[repo]) != 0 {
		t.Fatal("--dry-run changed GitHub")
	}

	h.mustRun("push")
	if h.rc.secrets[repo]["A"].Value != "a1" || h.rc.vars[repo]["V"].Value != "v1" {
		t.Fatalf("after push: %+v %+v", h.rc.secrets, h.rc.vars)
	}
	if got := ops(h.mustRun("status")); got["secret A"] != "ok" || got["var V"] != "ok" {
		t.Fatalf("status after push = %v", got)
	}
	it, _ := h.st.Get(store.Secret, repo, "A")
	if it.Meta.SyncedAt.IsZero() || !it.Meta.GitHubUpdatedAt.Equal(h.rc.secrets[repo]["A"].UpdatedAt) {
		t.Errorf("sync times not recorded: %+v", it.Meta)
	}

	// A second push with no changes sends nothing.
	before := h.rc.secrets[repo]["A"].UpdatedAt
	h.mustRun("push")
	if !h.rc.secrets[repo]["A"].UpdatedAt.Equal(before) {
		t.Error("unchanged secret was pushed again")
	}

	// Local change.
	h.mustRun("set", "A", "-b", "a2")
	if got := ops(h.mustRun("status")); got["secret A"] != "update" {
		t.Errorf("status after local change = %v", got)
	}
	h.mustRun("push")
	if h.rc.secrets[repo]["A"].Value != "a2" {
		t.Error("local change not pushed")
	}

	// Drift: changed on GitHub after the last push.
	h.rc.put(h.rc.secrets, repo, "A", "changed-on-github")
	h.rc.put(h.rc.vars, repo, "V", "changed-on-github")
	if err := h.run("status"); err == nil {
		t.Error("status should exit non-zero on drift")
	}
	if got := ops(h.stdout.String()); got["secret A"] != "drift" || got["var V"] != "drift" {
		t.Errorf("drift status = %v", got)
	}
	if err := h.run("push"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("push on drift = %v", err)
	}
	if h.rc.secrets[repo]["A"].Value != "changed-on-github" {
		t.Error("drifted secret was overwritten without --force")
	}
	h.mustRun("push", "--force")
	if h.rc.secrets[repo]["A"].Value != "a2" || h.rc.vars[repo]["V"].Value != "v1" {
		t.Error("--force did not overwrite")
	}

	// Untracked and prune; --secrets-only leaves variables alone.
	h.rc.put(h.rc.secrets, repo, "ROGUE", "x")
	h.rc.put(h.rc.vars, repo, "ROGUE_VAR", "x")
	if got := ops(h.mustRun("status")); got["secret ROGUE"] != "untracked" || got["var ROGUE_VAR"] != "untracked" {
		t.Errorf("untracked status = %v", got)
	}
	h.mustRun("push")
	if _, ok := h.rc.secrets[repo]["ROGUE"]; !ok {
		t.Error("push without --prune deleted an untracked secret")
	}
	h.mustRun("push", "--prune", "--secrets-only")
	if _, ok := h.rc.secrets[repo]["ROGUE"]; ok {
		t.Error("--prune did not delete the untracked secret")
	}
	if _, ok := h.rc.vars[repo]["ROGUE_VAR"]; !ok {
		t.Error("--secrets-only pruned a variable")
	}
	h.mustRun("push", "--prune", "--vars-only")
	if _, ok := h.rc.vars[repo]["ROGUE_VAR"]; ok {
		t.Error("--prune did not delete the untracked variable")
	}
}

func TestPushPruneGuard(t *testing.T) {
	h := setup(t)
	h.rc.put(h.rc.secrets, "github.com/o/unmanaged", "KEEP", "x")
	h.mustRun("push", "o/unmanaged", "--prune")
	if _, ok := h.rc.secrets["github.com/o/unmanaged"]["KEEP"]; !ok {
		t.Error("--prune wiped a scope with nothing in the keychain")
	}
	if !strings.Contains(h.stderr.String(), "not pruning secrets") {
		t.Errorf("stderr = %s", h.stderr.String())
	}
}

func TestPushAll(t *testing.T) {
	h := setup(t)
	h.mustRun("set", "o/a", "X", "-b", "1")
	h.mustRun("set", "o/b/env/prod", "Y", "-b", "2")
	h.mustRun("set", "o", "Z", "-b", "3")
	h.mustRun("push", "--all")
	if h.rc.secrets["github.com/o/a"]["X"].Value != "1" || h.rc.secrets["github.com/o/b/env/prod"]["Y"].Value != "2" ||
		h.rc.secrets["github.com/o"]["Z"].Value != "3" {
		t.Errorf("push --all = %+v", h.rc.secrets)
	}
	if err := h.run("push", "--all", "o/a"); err == nil {
		t.Error("push --all with a scope should fail")
	}
}

func TestEmptyValueNeverPushed(t *testing.T) {
	h := setup(t)
	h.rc.put(h.rc.secrets, repo, "REAL", "real-value")
	// An empty item created outside gh sk (e.g. in Keychain Access).
	_ = h.st.Set(store.Item{Kind: store.Secret, Scope: repo, Name: "REAL"})
	if err := h.run("push", "--force"); err == nil {
		t.Error("push of an empty value should fail")
	}
	if h.rc.secrets[repo]["REAL"].Value != "real-value" {
		t.Error("empty value overwrote the secret on GitHub")
	}
}

func TestPull(t *testing.T) {
	h := setup(t)
	h.rc.put(h.rc.vars, repo, "REGION", "us")
	h.rc.put(h.rc.vars, repo, "TIER", "gold")
	h.mustRun("pull")
	if h.value(store.Variable, repo, "REGION") != "us" || h.value(store.Variable, repo, "TIER") != "gold" {
		t.Fatal("pull did not store variables")
	}
	if got := ops(h.mustRun("status", "--vars-only")); got["var REGION"] != "ok" {
		t.Errorf("status after pull = %v", got)
	}

	// A local edit that was not pushed yet is protected.
	h.mustRun("set", "REGION", "--var", "-b", "local-edit")
	h.rc.put(h.rc.vars, repo, "REGION", "remote-edit")
	if err := h.run("pull"); err == nil {
		t.Error("pull should refuse to overwrite an unpushed local edit")
	}
	if h.value(store.Variable, repo, "REGION") != "local-edit" {
		t.Error("pull overwrote an unpushed local edit")
	}
	h.mustRun("pull", "--force")
	if h.value(store.Variable, repo, "REGION") != "remote-edit" {
		t.Error("pull --force did not overwrite")
	}
}

func TestPullSecretsTemplate(t *testing.T) {
	h := setup(t)
	h.rc.put(h.rc.secrets, repo, "TRACKED", "t")
	h.rc.put(h.rc.secrets, repo, "B_UNTRACKED", "b")
	h.rc.put(h.rc.secrets, repo, "A_UNTRACKED", "a")
	h.mustRun("set", "TRACKED", "-b", "t")

	file := filepath.Join(t.TempDir(), "secrets.env")
	h.mustRun("pull", "-f", file)
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("template mode = %v, %v", info.Mode().Perm(), err)
	}
	content, _ := os.ReadFile(file)
	if !strings.HasSuffix(string(content), "A_UNTRACKED=\nB_UNTRACKED=\n") || strings.Contains(string(content), "TRACKED=\n"+"TRACKED") ||
		strings.Contains(string(content), "\nTRACKED=") {
		t.Errorf("template =\n%s", content)
	}
	if err := h.run("pull", "-f", file); err == nil {
		t.Error("pull -f must not overwrite an existing file")
	}

	// The user's flow: pull -f, import without filling anything in, push.
	h.mustRun("import", "-f", file)
	h.mustRun("push", "--force")
	if h.rc.secrets[repo]["A_UNTRACKED"].Value != "a" || h.rc.secrets[repo]["B_UNTRACKED"].Value != "b" {
		t.Error("an unfilled template emptied secrets on GitHub")
	}

	// Filling one value in tracks it.
	if err := os.WriteFile(file, []byte("A_UNTRACKED=filled\nB_UNTRACKED=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.mustRun("import", "-f", file)
	h.mustRun("push")
	if h.rc.secrets[repo]["A_UNTRACKED"].Value != "filled" || h.rc.secrets[repo]["B_UNTRACKED"].Value != "b" {
		t.Errorf("after filling: %+v", h.rc.secrets[repo])
	}

	none := filepath.Join(t.TempDir(), "none.env")
	h.mustRun("set", "B_UNTRACKED", "-b", "b")
	h.mustRun("pull", "-f", none)
	if _, err := os.Stat(none); !os.IsNotExist(err) {
		t.Error("template written although every secret is tracked")
	}
}

func TestParseDotenv(t *testing.T) {
	got, err := parseDotenv(strings.NewReader("A=1\n#c\n\nexport B = '2'\nC=\"3\"\n"))
	if err != nil || len(got) != 3 || got["A"] != "1" || got["B"] != "2" || got["C"] != "3" {
		t.Errorf("parseDotenv = %v, %v", got, err)
	}
	if _, err := parseDotenv(strings.NewReader("A=1\nB\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("missing '=' error = %v", err)
	}
}
