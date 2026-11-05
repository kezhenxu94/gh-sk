// Package pusher plans and applies pushing keychain items to GitHub.
package pusher

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kezhenxu94/gh-sk/internal/remote"
	"github.com/kezhenxu94/gh-sk/internal/scope"
	"github.com/kezhenxu94/gh-sk/internal/store"
)

type Op string

const (
	Create    Op = "create"    // in keychain, not on GitHub
	Update    Op = "update"    // changed locally (or never synced by gh sk)
	OK        Op = "ok"        // in sync
	Drift     Op = "drift"     // changed on GitHub since our last sync; skipped unless forced
	Untracked Op = "untracked" // on GitHub, not in keychain
	Delete    Op = "delete"    // untracked and --prune given
)

type Action struct {
	Op    Op
	Kind  store.Kind
	Scope scope.Scope
	Name  string
	Local *store.Item // nil for Untracked/Delete
}

// Changes reports whether the action modifies GitHub.
func (a Action) Changes() bool { return a.Op == Create || a.Op == Update || a.Op == Delete }

// Plan compares local items (variables must have Value loaded) of one scope
// and kind with what GitHub reports.
func Plan(s scope.Scope, kind store.Kind, local []store.Item, remoteEntries map[string]remote.Entry, prune, force bool) []Action {
	var acts []Action
	seen := map[string]bool{}
	for i := range local {
		it := &local[i]
		seen[it.Name] = true
		a := Action{Kind: kind, Scope: s, Name: it.Name, Local: it}
		r, exists := remoteEntries[it.Name]
		drifted := !it.Meta.GitHubUpdatedAt.IsZero() &&
			r.UpdatedAt.Truncate(time.Second).After(it.Meta.GitHubUpdatedAt.Truncate(time.Second))
		switch {
		case !exists:
			a.Op = Create
		case kind == store.Variable && it.Value == r.Value:
			a.Op = OK
		case drifted && !force:
			a.Op = Drift
		case drifted || kind == store.Variable || it.ChangedSinceSync():
			a.Op = Update
		default:
			a.Op = OK
		}
		acts = append(acts, a)
	}
	for name := range remoteEntries {
		if seen[name] {
			continue
		}
		op := Untracked
		if prune {
			op = Delete
		}
		acts = append(acts, Action{Op: op, Kind: kind, Scope: s, Name: name})
	}
	sort.Slice(acts, func(i, j int) bool { return acts[i].Name < acts[j].Name })
	return acts
}

// Apply executes the changing actions of one scope and kind, then records
// sync timestamps on every pushed keychain item.
func Apply(ctx context.Context, st store.Store, rc remote.Remote, s scope.Scope, kind store.Kind, acts []Action) error {
	var errs []error
	var pushed []*store.Item
	for _, a := range acts {
		var err error
		switch a.Op {
		case Create, Update:
			value := a.Local.Value
			if kind == store.Secret {
				var it store.Item
				if it, err = st.Get(kind, a.Local.Scope, a.Name); err != nil {
					break
				}
				value = it.Value
			}
			if value == "" {
				// Last line of defence: never overwrite a value on GitHub with nothing.
				err = errors.New("refusing to push an empty value; set it with `gh sk set`")
				break
			}
			if kind == store.Secret {
				err = rc.PutSecret(ctx, s, a.Name, value, a.Local.Meta)
			} else {
				err = rc.PutVar(ctx, s, a.Name, value, a.Op == Update, a.Local.Meta)
			}
			if err == nil {
				pushed = append(pushed, a.Local)
			}
		case Delete:
			if kind == store.Secret {
				err = rc.DeleteSecret(ctx, s, a.Name)
			} else {
				err = rc.DeleteVar(ctx, s, a.Name)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s %s: %w", a.Op, kind.Short(), a.Name, err))
		}
	}
	if len(pushed) > 0 {
		errs = append(errs, record(ctx, st, rc, s, kind, pushed))
	}
	return errors.Join(errs...)
}

// record re-reads GitHub's updated_at for pushed items and stores it with the sync time.
func record(ctx context.Context, st store.Store, rc remote.Remote, s scope.Scope, kind store.Kind, pushed []*store.Item) error {
	list := rc.ListSecrets
	if kind == store.Variable {
		list = rc.ListVars
	}
	entries, err := list(ctx, s)
	if err != nil {
		return fmt.Errorf("record sync time: %w", err)
	}
	now := time.Now()
	var errs []error
	for _, it := range pushed {
		m := it.Meta
		m.SyncedAt = now
		m.GitHubUpdatedAt = entries[it.Name].UpdatedAt
		errs = append(errs, st.SetMeta(kind, it.Scope, it.Name, m))
	}
	return errors.Join(errs...)
}
