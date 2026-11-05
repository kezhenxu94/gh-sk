// Package store keeps GitHub secrets and variables in the macOS Keychain.
package store

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Kind string

const (
	Secret   Kind = "github-secret"
	Variable Kind = "github-variable"
)

func (k Kind) Short() string {
	if k == Variable {
		return "var"
	}
	return "secret"
}

// Meta is stored as JSON in the keychain item comment.
type Meta struct {
	// Org-level visibility: all, private or selected (with Repos).
	Visibility string   `json:"visibility,omitempty"`
	Repos      []string `json:"repos,omitempty"`
	// SyncedAt is when gh sk last pushed this item to GitHub.
	SyncedAt time.Time `json:"synced_at,omitzero"`
	// GitHubUpdatedAt is GitHub's updated_at recorded right after that push.
	GitHubUpdatedAt time.Time `json:"github_updated_at,omitzero"`
}

type Item struct {
	Kind     Kind
	Scope    string // e.g. github.com/org/repo
	Name     string
	Meta     Meta
	Modified time.Time // keychain modification date
	Value    string    // only populated by Get
}

// localSlack absorbs the gap between recording SyncedAt and the keychain
// writing its (second-granularity) modification date. It only matters for
// edits made outside gh sk (e.g. in Keychain Access); gh sk itself clears SyncedAt.
const localSlack = 5 * time.Second

// ChangedSinceSync reports whether the value was modified after the last sync.
func (it Item) ChangedSinceSync() bool {
	return it.Meta.SyncedAt.IsZero() || it.Modified.After(it.Meta.SyncedAt.Add(localSlack))
}

var ErrNotFound = errors.New("not found in keychain")

type Store interface {
	// List returns items of the given kind without values (no keychain prompt).
	List(kind Kind) ([]Item, error)
	Get(kind Kind, scope, name string) (Item, error)
	// Set writes value and meta. It keeps the GitHubUpdatedAt (so drift is
	// still detected) and, when none is given, the visibility of an existing
	// item. SyncedAt is stored as given: callers
	// changing the value pass zero, marking the item as changed since sync.
	Set(item Item) error
	// SetMeta updates only the metadata of an existing item.
	SetMeta(kind Kind, scope, name string, meta Meta) error
	Delete(kind Kind, scope, name string) error
}

var nameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// NormalizeName upper-cases the name (GitHub does the same) and validates it.
func NormalizeName(name string) (string, error) {
	n := strings.ToUpper(name)
	if !nameRe.MatchString(n) || strings.HasPrefix(n, "GITHUB_") {
		return "", fmt.Errorf("invalid name %q: use letters, digits and _, not starting with a digit or GITHUB_", name)
	}
	return n, nil
}
