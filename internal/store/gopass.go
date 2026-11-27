package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/gopasspw/gopass/pkg/ctxutil"
	"github.com/gopasspw/gopass/pkg/gopass"
	"github.com/gopasspw/gopass/pkg/gopass/api"
)

// gopassPrefix is the folder of the password store that holds every gh sk entry.
const gopassPrefix = "gh-sk/"

// gopassEntry is the content of one entry. It is JSON because gopass
// normalizes line endings of raw secrets, and values must round-trip exactly.
// Modified is stored explicitly because file times do not survive git syncing.
type gopassEntry struct {
	Kind     Kind      `json:"kind"`
	Modified time.Time `json:"modified"`
	Meta     Meta      `json:"meta,omitzero"`
	Value    string    `json:"value"`
}

func (e gopassEntry) Bytes() []byte {
	b, _ := json.MarshalIndent(e, "", "  ")
	return append(b, '\n')
}

// Gopass stores each item as the entry gh-sk/<scope>/<NAME> of the gopass
// store (or a pass store: same format), through the gopass library and gpg.
// Syncing is left to gopass: core.autopush / `gopass sync`.
type Gopass struct{ s gopass.Store }

// gopassCtx reads entries raw, without gopass' secret parsing.
var gopassCtx = ctxutil.WithShowParsing(context.Background(), false)

func NewGopass() (Gopass, error) {
	// The api's background queue is only enabled through the context, which
	// gopassCtx does not do, so commits run inline and Close is not needed.
	s, err := api.New(gopassCtx)
	if err != nil {
		return Gopass{}, fmt.Errorf("open gopass store: %w", err)
	}
	return Gopass{s}, nil
}

// entryName returns the entry of scope/name, rejecting paths that would escape the prefix.
func entryName(scope, name string) (string, error) {
	p := gopassPrefix + scope + "/" + name
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid gopass entry %q", p)
		}
	}
	return p, nil
}

// exists lists entries (no decryption) instead of matching Get's unexported not-found error.
func (g Gopass) exists(entry string) (bool, error) {
	names, err := g.s.List(gopassCtx)
	if err != nil {
		return false, fmt.Errorf("list gopass entries: %w", err)
	}
	for _, n := range names {
		if n == entry {
			return true, nil
		}
	}
	return false, nil
}

func (g Gopass) read(entry string) (Item, error) {
	sec, err := g.s.Get(gopassCtx, entry, "latest")
	if err != nil {
		return Item{}, fmt.Errorf("read gopass entry %s: %w", entry, err)
	}
	var e gopassEntry
	if err := json.Unmarshal(sec.Bytes(), &e); err != nil {
		return Item{}, fmt.Errorf("gopass entry %s was not written by gh sk: %w", entry, err)
	}
	if e.Kind != Secret && e.Kind != Variable {
		return Item{}, fmt.Errorf("gopass entry %s: unknown kind %q", entry, e.Kind)
	}
	rel := strings.TrimPrefix(entry, gopassPrefix)
	return Item{Kind: e.Kind, Scope: path.Dir(rel), Name: path.Base(rel), Meta: e.Meta, Modified: e.Modified, Value: e.Value}, nil
}

// lookup returns scope/name, or nil if it does not exist.
func (g Gopass) lookup(scope, name string) (*Item, error) {
	entry, err := entryName(scope, name)
	if err != nil {
		return nil, err
	}
	if ok, err := g.exists(entry); err != nil || !ok {
		return nil, err
	}
	it, err := g.read(entry)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func (g Gopass) lookupKind(kind Kind, scope, name string) (Item, error) {
	it, err := g.lookup(scope, name)
	if err != nil {
		return Item{}, err
	}
	if it == nil || it.Kind != kind {
		return Item{}, fmt.Errorf("%s %s/%s: %w", kind.Short(), scope, name, ErrNotFound)
	}
	return *it, nil
}

func (g Gopass) write(ctx context.Context, it Item) error {
	entry, err := entryName(it.Scope, it.Name)
	if err != nil {
		return err
	}
	e := gopassEntry{Kind: it.Kind, Modified: it.Modified.UTC(), Meta: it.Meta, Value: it.Value}
	if err := g.s.Set(ctx, entry, e); err != nil {
		return fmt.Errorf("save gopass entry %s: %w", entry, err)
	}
	return nil
}

// List decrypts every entry, as kind and metadata are encrypted with the value.
func (g Gopass) List(kind Kind) ([]Item, error) {
	names, err := g.s.List(gopassCtx)
	if err != nil {
		return nil, fmt.Errorf("list gopass entries: %w", err)
	}
	var items []Item
	for _, n := range names {
		if !strings.HasPrefix(n, gopassPrefix) {
			continue
		}
		it, err := g.read(n)
		if err != nil {
			return nil, err
		}
		if it.Kind == kind {
			it.Value = ""
			items = append(items, it)
		}
	}
	return items, nil
}

func (g Gopass) Get(kind Kind, scope, name string) (Item, error) {
	return g.lookupKind(kind, scope, name)
}

func (g Gopass) Set(it Item) error { return g.set(gopassCtx, it) }

func (g Gopass) set(ctx context.Context, it Item) error {
	old, err := g.lookup(it.Scope, it.Name)
	if err != nil {
		return err
	}
	if it, err = merge(old, it); err != nil {
		return err
	}
	it.Modified = time.Now()
	return g.write(ctx, it)
}

// setAll stages every item but the last without committing; the last write
// then commits them all, so core.autopush pushes once instead of per item.
func (g Gopass) setAll(items []Item) error {
	for i, it := range items {
		ctx := ctxutil.WithGitCommit(gopassCtx, false)
		if i == len(items)-1 {
			ctx = ctxutil.WithCommitMessage(gopassCtx, fmt.Sprintf("gh sk: save %d item(s)", len(items)))
		}
		if err := g.set(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

// SetMeta keeps Modified, which tracks the value only.
func (g Gopass) SetMeta(kind Kind, scope, name string, meta Meta) error {
	it, err := g.lookupKind(kind, scope, name)
	if err != nil {
		return err
	}
	it.Meta = meta
	return g.write(gopassCtx, it)
}

func (g Gopass) Delete(kind Kind, scope, name string) error {
	if _, err := g.lookupKind(kind, scope, name); err != nil {
		return err
	}
	entry, _ := entryName(scope, name)
	if err := g.s.Remove(gopassCtx, entry); err != nil {
		return fmt.Errorf("delete gopass entry %s: %w", entry, err)
	}
	return nil
}
