package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/kezhenxu94/gh-sk/internal/remote"
	"github.com/kezhenxu94/gh-sk/internal/scope"
	"github.com/kezhenxu94/gh-sk/internal/store"
)

var (
	sc = scope.Scope{Org: "o", Repo: "r"}
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

func ops(acts []Action) map[string]Op {
	m := map[string]Op{}
	for _, a := range acts {
		m[a.Name] = a.Op
	}
	return m
}

func TestPlanSecrets(t *testing.T) {
	synced := store.Meta{SyncedAt: t0, GitHubUpdatedAt: t0}
	local := []store.Item{
		{Name: "NEW"},
		{Name: "NEVER_SYNCED"},
		{Name: "IN_SYNC", Meta: synced, Modified: t0},
		{Name: "LOCAL_EDIT", Meta: synced, Modified: t0.Add(time.Hour)},
		{Name: "SET_BY_GHSK", Meta: store.Meta{GitHubUpdatedAt: t0}, Modified: t0}, // Set clears SyncedAt
		{Name: "DRIFT", Meta: synced, Modified: t0},
	}
	rem := map[string]remote.Entry{
		"NEVER_SYNCED": {UpdatedAt: t0},
		"IN_SYNC":      {UpdatedAt: t0},
		"LOCAL_EDIT":   {UpdatedAt: t0},
		"SET_BY_GHSK":  {UpdatedAt: t0},
		"DRIFT":        {UpdatedAt: t0.Add(time.Minute)},
		"EXTRA":        {UpdatedAt: t0},
	}
	want := map[string]Op{
		"NEW": Create, "NEVER_SYNCED": Update, "IN_SYNC": OK, "LOCAL_EDIT": Update,
		"SET_BY_GHSK": Update, "DRIFT": Drift, "EXTRA": Untracked,
	}
	assertOps(t, ops(Plan(sc, store.Secret, local, rem, false, false)), want)

	want["DRIFT"], want["EXTRA"] = Update, Delete
	assertOps(t, ops(Plan(sc, store.Secret, local, rem, true, true)), want)
}

func TestPlanVariables(t *testing.T) {
	synced := store.Meta{SyncedAt: t0, GitHubUpdatedAt: t0}
	local := []store.Item{
		{Name: "SAME", Value: "a", Meta: synced},
		{Name: "DIFF", Value: "b", Meta: synced, Modified: t0},
		{Name: "DIFF_DRIFT", Value: "b", Meta: synced},
	}
	rem := map[string]remote.Entry{
		"SAME":       {Value: "a", UpdatedAt: t0.Add(time.Hour)},
		"DIFF":       {Value: "x", UpdatedAt: t0},
		"DIFF_DRIFT": {Value: "x", UpdatedAt: t0.Add(time.Hour)},
	}
	assertOps(t, ops(Plan(sc, store.Variable, local, rem, false, false)),
		map[string]Op{"SAME": OK, "DIFF": Update, "DIFF_DRIFT": Drift})
}

func assertOps(t *testing.T, got, want map[string]Op) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %s, want %s", k, got[k], v)
		}
	}
}

type fakeStore struct {
	items map[string]store.Item
}

func (f *fakeStore) List(store.Kind) ([]store.Item, error) { return nil, nil }
func (f *fakeStore) Get(_ store.Kind, _, name string) (store.Item, error) {
	return f.items[name], nil
}
func (f *fakeStore) Set(it store.Item) error { f.items[it.Name] = it; return nil }
func (f *fakeStore) SetMeta(_ store.Kind, _, name string, m store.Meta) error {
	it := f.items[name]
	it.Meta = m
	f.items[name] = it
	return nil
}
func (f *fakeStore) Delete(store.Kind, string, string) error { return nil }

type fakeRemote struct {
	secrets map[string]remote.Entry
	values  map[string]string
}

func (f *fakeRemote) ListSecrets(context.Context, scope.Scope) (map[string]remote.Entry, error) {
	return f.secrets, nil
}
func (f *fakeRemote) PutSecret(_ context.Context, _ scope.Scope, name, value string, _ store.Meta) error {
	f.values[name] = value
	f.secrets[name] = remote.Entry{UpdatedAt: t0.Add(2 * time.Hour)}
	return nil
}
func (f *fakeRemote) DeleteSecret(_ context.Context, _ scope.Scope, name string) error {
	delete(f.secrets, name)
	return nil
}
func (f *fakeRemote) ListVars(context.Context, scope.Scope) (map[string]remote.Entry, error) {
	return nil, nil
}
func (f *fakeRemote) PutVar(context.Context, scope.Scope, string, string, bool, store.Meta) error {
	return nil
}
func (f *fakeRemote) DeleteVar(context.Context, scope.Scope, string) error { return nil }

func TestApplyRecordsSyncTimes(t *testing.T) {
	st := &fakeStore{items: map[string]store.Item{"A": {Name: "A", Value: "secret-a"}}}
	rc := &fakeRemote{secrets: map[string]remote.Entry{"OLD": {UpdatedAt: t0}}, values: map[string]string{}}
	local := []store.Item{{Name: "A"}}
	acts := Plan(sc, store.Secret, local, rc.secrets, true, false)
	if err := Apply(context.Background(), st, rc, sc, store.Secret, acts); err != nil {
		t.Fatal(err)
	}
	if rc.values["A"] != "secret-a" {
		t.Errorf("pushed value = %q", rc.values["A"])
	}
	if _, ok := rc.secrets["OLD"]; ok {
		t.Error("OLD should be pruned")
	}
	m := st.items["A"].Meta
	if m.SyncedAt.IsZero() || !m.GitHubUpdatedAt.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("sync times not recorded: %+v", m)
	}
}
