package store

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{"npm_token": "NPM_TOKEN", "_X1": "_X1", "A": "A"} {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Errorf("NormalizeName(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "1A", "A-B", "A B", "github_token", "GITHUB_X", "É"} {
		if _, err := NormalizeName(in); err == nil {
			t.Errorf("NormalizeName(%q) should fail", in)
		}
	}
}

func TestChangedSinceSync(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		it   Item
		want bool
	}{
		{"never synced", Item{Modified: t0}, true},
		{"modified at sync", Item{Modified: t0.Add(time.Second), Meta: Meta{SyncedAt: t0}}, false},
		{"within slack", Item{Modified: t0.Add(localSlack), Meta: Meta{SyncedAt: t0}}, false},
		{"after slack", Item{Modified: t0.Add(localSlack + time.Second), Meta: Meta{SyncedAt: t0}}, true},
	}
	for _, c := range cases {
		if got := c.it.ChangedSinceSync(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMemory(t *testing.T) { testStore(t, NewMemory(), "github.com/o/r") }

// TestKeychain runs the same contract against the real macOS Keychain. It
// writes to the default keychain, so it only runs with GHSK_KEYCHAIN_TESTS=1
// (CI uses a throwaway keychain).
func TestKeychain(t *testing.T) {
	if os.Getenv("GHSK_KEYCHAIN_TESTS") != "1" {
		t.Skip("set GHSK_KEYCHAIN_TESTS=1 to run against the real keychain")
	}
	scope := "github.com/gh-sk-test/" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "-" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		for _, k := range []Kind{Secret, Variable} {
			items, _ := Keychain{}.List(k)
			for _, it := range items {
				if it.Scope == scope {
					_ = Keychain{}.Delete(k, it.Scope, it.Name)
				}
			}
		}
	})
	testStore(t, Keychain{}, scope)
}

// testStore is the behaviour every Store must have.
func testStore(t *testing.T, s Store, scope string) {
	t.Helper()
	mustSet := func(it Item) {
		t.Helper()
		if err := s.Set(it); err != nil {
			t.Fatalf("Set %s: %v", it.Name, err)
		}
	}
	listed := func(kind Kind) map[string]Item {
		t.Helper()
		items, err := s.List(kind)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]Item{}
		for _, it := range items {
			if it.Scope == scope {
				m[it.Name] = it
			}
		}
		return m
	}

	value := "multi\nline ✓ 'quoted' \"x\" #=$"
	mustSet(Item{Kind: Secret, Scope: scope, Name: "S1", Value: value})
	mustSet(Item{Kind: Variable, Scope: scope, Name: "V1", Value: "v"})

	// Get returns the exact value; List never returns values.
	if it, err := s.Get(Secret, scope, "S1"); err != nil || it.Value != value || it.Kind != Secret {
		t.Fatalf("Get S1 = %+v, %v", it, err)
	}
	secrets := listed(Secret)
	if len(secrets) != 1 || secrets["S1"].Value != "" || secrets["S1"].Modified.IsZero() {
		t.Fatalf("List secrets = %+v", secrets)
	}
	if vars := listed(Variable); len(vars) != 1 || vars["V1"].Name != "V1" {
		t.Fatalf("List vars = %+v", vars)
	}

	// Kinds are separate namespaces for lookups but share the keychain key.
	if _, err := s.Get(Variable, scope, "S1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get with wrong kind: %v", err)
	}
	if err := s.Set(Item{Kind: Variable, Scope: scope, Name: "S1", Value: "x"}); err == nil {
		t.Error("Set of a variable over a secret should fail")
	}
	if _, err := s.Get(Secret, scope, "MISSING"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: %v", err)
	}

	// SetMeta changes metadata only.
	synced := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	meta := Meta{SyncedAt: synced, GitHubUpdatedAt: synced, Visibility: "selected", Repos: []string{"a", "b"}}
	if err := s.SetMeta(Secret, scope, "S1", meta); err != nil {
		t.Fatal(err)
	}
	it, err := s.Get(Secret, scope, "S1")
	if err != nil || it.Value != value || !it.Meta.SyncedAt.Equal(synced) || it.Meta.Visibility != "selected" || len(it.Meta.Repos) != 2 {
		t.Fatalf("after SetMeta: %+v, %v", it, err)
	}
	if err := s.SetMeta(Variable, scope, "S1", meta); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMeta with wrong kind: %v", err)
	}

	// Set keeps GitHubUpdatedAt and visibility when not given, and takes SyncedAt as given.
	mustSet(Item{Kind: Secret, Scope: scope, Name: "S1", Value: "new"})
	it, _ = s.Get(Secret, scope, "S1")
	if it.Value != "new" || !it.Meta.GitHubUpdatedAt.Equal(synced) || !it.Meta.SyncedAt.IsZero() || it.Meta.Visibility != "selected" {
		t.Fatalf("after re-Set: %+v", it.Meta)
	}
	if !it.ChangedSinceSync() {
		t.Error("re-Set item must count as changed since sync")
	}
	mustSet(Item{Kind: Secret, Scope: scope, Name: "S1", Value: "new", Meta: Meta{Visibility: "all"}})
	if it, _ = s.Get(Secret, scope, "S1"); it.Meta.Visibility != "all" || len(it.Meta.Repos) != 0 {
		t.Fatalf("explicit visibility not applied: %+v", it.Meta)
	}

	// Delete checks the kind and removes the item.
	if err := s.Delete(Variable, scope, "S1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete with wrong kind: %v", err)
	}
	for _, d := range []struct {
		k Kind
		n string
	}{{Secret, "S1"}, {Variable, "V1"}} {
		if err := s.Delete(d.k, scope, d.n); err != nil {
			t.Fatal(err)
		}
	}
	if len(listed(Secret))+len(listed(Variable)) != 0 {
		t.Error("items left after Delete")
	}
}
