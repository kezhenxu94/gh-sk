package remote

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"
	"golang.org/x/crypto/nacl/box"
)

// fakeGitHub is a minimal in-memory implementation of the Actions secrets and
// variables REST API. Secret values are decrypted with the private key, so
// tests can check exactly what GitHub would store.
type fakeGitHub struct {
	t        *testing.T
	pub      *[32]byte
	priv     *[32]byte
	pageSize int
	status   int // when non-zero, every request fails with this status

	mu      sync.Mutex
	clock   time.Time
	secrets map[string]map[string]fakeEntry // base -> name -> entry
	vars    map[string]map[string]fakeEntry
	bodies  map[string]map[string]any // "METHOD path" -> last JSON body
	repoIDs map[string]int64          // "owner/repo" -> id
}

type fakeEntry struct {
	Value     string
	UpdatedAt time.Time
}

var (
	apiRe  = regexp.MustCompile(`^/(repos/[^/]+/[^/]+/actions|orgs/[^/]+/actions|repos/[^/]+/[^/]+/environments/[^/]+)/(secrets|variables)(?:/([^/]+))?$`)
	repoRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)$`)
)

func newFake(t *testing.T) (*fakeGitHub, *Client) {
	t.Helper()
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{
		t: t, pub: pub, priv: priv, pageSize: 2,
		clock:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		secrets: map[string]map[string]fakeEntry{},
		vars:    map[string]map[string]fakeEntry{},
		bodies:  map[string]map[string]any{},
		repoIDs: map[string]int64{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	u := srv.URL + "/"
	gh, err := github.NewClient(github.WithURLs(&u, &u))
	if err != nil {
		t.Fatal(err)
	}
	return f, &Client{gh: gh}
}

func (f *fakeGitHub) tick() time.Time {
	f.clock = f.clock.Add(time.Minute)
	return f.clock
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"message":"denied"}`))
		return
	}
	var body map[string]any
	if r.Body != nil && (r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodPatch) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies[r.Method+" "+r.URL.Path] = body
	}
	if m := repoRe.FindStringSubmatch(r.URL.Path); m != nil && r.Method == http.MethodGet {
		id, ok := f.repoIDs[m[1]+"/"+m[2]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"id": id, "name": m[2]})
		return
	}
	m := apiRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	base, kind, name := m[1], m[2], m[3]
	store := f.secrets
	if kind == "variables" {
		store = f.vars
	}
	if store[base] == nil {
		store[base] = map[string]fakeEntry{}
	}
	entries := store[base]

	switch {
	case kind == "secrets" && name == "public-key" && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{"key_id": "kid-1", "key": base64.StdEncoding.EncodeToString(f.pub[:])})
	case name == "" && r.Method == http.MethodGet:
		f.list(w, r, kind, entries)
	case kind == "secrets" && r.Method == http.MethodPut:
		if body["key_id"] != "kid-1" {
			f.t.Errorf("PUT %s: key_id = %v", r.URL.Path, body["key_id"])
		}
		raw, _ := base64.StdEncoding.DecodeString(fmt.Sprint(body["encrypted_value"]))
		plain, ok := box.OpenAnonymous(nil, raw, f.pub, f.priv)
		if !ok {
			f.t.Errorf("PUT %s: value does not decrypt", r.URL.Path)
		}
		entries[name] = fakeEntry{Value: string(plain), UpdatedAt: f.tick()}
		w.WriteHeader(http.StatusCreated)
	case kind == "variables" && name == "" && r.Method == http.MethodPost:
		n := fmt.Sprint(body["name"])
		if _, exists := entries[n]; exists {
			w.WriteHeader(http.StatusConflict)
			return
		}
		entries[n] = fakeEntry{Value: fmt.Sprint(body["value"]), UpdatedAt: f.tick()}
		w.WriteHeader(http.StatusCreated)
	case kind == "variables" && r.Method == http.MethodPatch:
		if _, exists := entries[name]; !exists {
			http.NotFound(w, r)
			return
		}
		entries[name] = fakeEntry{Value: fmt.Sprint(body["value"]), UpdatedAt: f.tick()}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		if _, exists := entries[name]; !exists {
			http.NotFound(w, r)
			return
		}
		delete(entries, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// list serves a page of f.pageSize items, ignoring per_page, with a Link header.
func (f *fakeGitHub) list(w http.ResponseWriter, r *http.Request, kind string, entries map[string]fakeEntry) {
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start, end := (page-1)*f.pageSize, page*f.pageSize
	if start > len(names) {
		start = len(names)
	}
	if end > len(names) {
		end = len(names)
	}
	if end < len(names) {
		next := *r.URL
		q := next.Query()
		q.Set("page", strconv.Itoa(page+1))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s>; rel="next"`, r.Host, next.String()))
	}
	items := []map[string]any{}
	for _, n := range names[start:end] {
		it := map[string]any{"name": n, "created_at": entries[n].UpdatedAt, "updated_at": entries[n].UpdatedAt}
		if kind == "variables" {
			it["value"] = entries[n].Value
		}
		items = append(items, it)
	}
	writeJSON(w, map[string]any{"total_count": len(names), kind: items})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
