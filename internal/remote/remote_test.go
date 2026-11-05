package remote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
	"golang.org/x/crypto/nacl/box"

	"github.com/kezhenxu94/gh-sk/internal/scope"
	"github.com/kezhenxu94/gh-sk/internal/store"
)

var ctx = context.Background()

var (
	repoScope = scope.Scope{Org: "o", Repo: "r"}
	envScope  = scope.Scope{Org: "o", Repo: "r", Env: "prod"}
	orgScope  = scope.Scope{Org: "o"}
)

func base(s scope.Scope) string {
	switch {
	case s.IsOrg():
		return "orgs/o/actions"
	case s.IsEnv():
		return "repos/o/r/environments/prod"
	}
	return "repos/o/r/actions"
}

func TestSealRoundTrip(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk := &github.PublicKey{Key: github.Ptr(base64.StdEncoding.EncodeToString(pub[:]))}
	enc, err := seal(pk, "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(enc)
	got, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok || string(got) != "s3cr3t" {
		t.Fatalf("OpenAnonymous = %q, %v", got, ok)
	}
	if _, err := seal(&github.PublicKey{Key: github.Ptr("bm90LWEta2V5")}, "x"); err == nil {
		t.Error("seal should reject a key that is not 32 bytes")
	}
}

func TestSecretsAllScopes(t *testing.T) {
	for _, s := range []scope.Scope{repoScope, envScope, orgScope} {
		t.Run(s.String(), func(t *testing.T) {
			f, c := newFake(t)
			value := "p@ss word\nwith 'quotes' ✓"
			if err := c.PutSecret(ctx, s, "TOKEN", value, store.Meta{}); err != nil {
				t.Fatal(err)
			}
			if got := f.secrets[base(s)]["TOKEN"].Value; got != value {
				t.Errorf("GitHub stored %q, want %q", got, value)
			}
			list, err := c.ListSecrets(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if e, ok := list["TOKEN"]; !ok || !e.UpdatedAt.Equal(f.secrets[base(s)]["TOKEN"].UpdatedAt) || e.Value != "" {
				t.Errorf("ListSecrets = %+v", list)
			}
			if err := c.DeleteSecret(ctx, s, "TOKEN"); err != nil {
				t.Fatal(err)
			}
			if _, ok := f.secrets[base(s)]["TOKEN"]; ok {
				t.Error("secret not deleted")
			}
		})
	}
}

func TestOrgSecretVisibility(t *testing.T) {
	f, c := newFake(t)
	f.repoIDs = map[string]int64{"o/a": 11, "o/b": 12}
	key := "PUT /orgs/o/actions/secrets/"

	if err := c.PutSecret(ctx, orgScope, "DEFAULT", "x", store.Meta{}); err != nil {
		t.Fatal(err)
	}
	if v := f.bodies[key+"DEFAULT"]["visibility"]; v != "private" {
		t.Errorf("default visibility = %v, want private", v)
	}

	if err := c.PutSecret(ctx, orgScope, "SEL", "x", store.Meta{Visibility: "selected", Repos: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	body := f.bodies[key+"SEL"]
	if body["visibility"] != "selected" || !reflect.DeepEqual(body["selected_repository_ids"], []any{11.0, 12.0}) {
		t.Errorf("selected body = %v", body)
	}

	err := c.PutSecret(ctx, orgScope, "BAD", "x", store.Meta{Visibility: "selected", Repos: []string{"missing"}})
	if err == nil || !strings.Contains(err.Error(), "o/missing") {
		t.Errorf("unknown repo error = %v", err)
	}
}

func TestListPagination(t *testing.T) {
	f, c := newFake(t)
	f.pageSize = 2
	want := []string{"A", "B", "C", "D", "E"}
	for _, n := range want {
		if err := c.PutSecret(ctx, repoScope, n, "v", store.Meta{}); err != nil {
			t.Fatal(err)
		}
		if err := c.PutVar(ctx, repoScope, n, "v-"+n, false, store.Meta{}); err != nil {
			t.Fatal(err)
		}
	}
	secrets, err := c.ListSecrets(ctx, repoScope)
	if err != nil || len(secrets) != len(want) {
		t.Fatalf("ListSecrets over pages = %d items, %v", len(secrets), err)
	}
	vars, err := c.ListVars(ctx, repoScope)
	if err != nil || len(vars) != len(want) || vars["E"].Value != "v-E" {
		t.Fatalf("ListVars over pages = %+v, %v", vars, err)
	}
}

func TestVariablesAllScopes(t *testing.T) {
	for _, s := range []scope.Scope{repoScope, envScope, orgScope} {
		t.Run(s.String(), func(t *testing.T) {
			f, c := newFake(t)
			if err := c.PutVar(ctx, s, "REGION", "us", false, store.Meta{}); err != nil {
				t.Fatal("create:", err)
			}
			if err := c.PutVar(ctx, s, "REGION", "eu", true, store.Meta{}); err != nil {
				t.Fatal("update:", err)
			}
			vars, err := c.ListVars(ctx, s)
			if err != nil || vars["REGION"].Value != "eu" || vars["REGION"].UpdatedAt.IsZero() {
				t.Fatalf("ListVars = %+v, %v", vars, err)
			}
			if s.IsOrg() {
				if v := f.bodies["POST /orgs/o/actions/variables"]["visibility"]; v != "private" {
					t.Errorf("org variable visibility = %v", v)
				}
			}
			if err := c.DeleteVar(ctx, s, "REGION"); err != nil {
				t.Fatal(err)
			}
			if vars, _ := c.ListVars(ctx, s); len(vars) != 0 {
				t.Errorf("variable not deleted: %+v", vars)
			}
		})
	}
}

func TestErrorHints(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		f, c := newFake(t)
		f.status = status
		if _, err := c.ListSecrets(ctx, repoScope); err == nil || !strings.Contains(err.Error(), `"repo" scope`) {
			t.Errorf("%d repo error = %v", status, err)
		}
		if _, err := c.ListVars(ctx, orgScope); err == nil || !strings.Contains(err.Error(), `"admin:org" scope`) {
			t.Errorf("%d org error = %v", status, err)
		}
		if err := c.PutSecret(ctx, envScope, "X", "v", store.Meta{}); err == nil || !strings.Contains(err.Error(), "github.com/o/r/env/prod") {
			t.Errorf("%d env error = %v", status, err)
		}
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "ghp_from_env")
	if tok, err := Token(); err != nil || tok != "ghp_from_env" {
		t.Errorf("Token() = %q, %v", tok, err)
	}
}
