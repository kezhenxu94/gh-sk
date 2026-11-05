package store

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/keybase/go-keychain"
)

// Keychain stores each item as a generic password:
// service = scope, account = name, description = kind, comment = Meta JSON.
type Keychain struct{}

func query(scope, name string) keychain.Item {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(scope)
	q.SetAccount(name)
	return q
}

func toItem(r keychain.QueryResult) Item {
	it := Item{Kind: Kind(r.Description), Scope: r.Service, Name: r.Account, Modified: r.ModificationDate, Value: string(r.Data)}
	if r.Comment != "" {
		_ = json.Unmarshal([]byte(r.Comment), &it.Meta)
	}
	return it
}

func encodeMeta(m Meta) string {
	b, _ := json.Marshal(m)
	if string(b) == "{}" {
		return ""
	}
	return string(b)
}

func (Keychain) List(kind Kind) ([]Item, error) {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetDescription(string(kind))
	q.SetMatchLimit(keychain.MatchLimitAll)
	q.SetReturnAttributes(true)
	res, err := keychain.QueryItem(q)
	if err != nil {
		return nil, fmt.Errorf("list keychain items: %w", err)
	}
	items := make([]Item, 0, len(res))
	for _, r := range res {
		if r.Description == string(kind) {
			items = append(items, toItem(r))
		}
	}
	return items, nil
}

// lookup returns the attributes (and optionally data) of scope/name, or nil if absent.
func lookup(scope, name string, data bool) (*Item, error) {
	q := query(scope, name)
	q.SetMatchLimit(keychain.MatchLimitOne)
	q.SetReturnAttributes(true)
	q.SetReturnData(data)
	res, err := keychain.QueryItem(q)
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", scope, name, err)
	}
	if len(res) == 0 {
		return nil, nil
	}
	it := toItem(res[0])
	return &it, nil
}

func lookupKind(kind Kind, scope, name string, data bool) (*Item, error) {
	it, err := lookup(scope, name, data)
	if err != nil {
		return nil, err
	}
	if it == nil || it.Kind != kind {
		return nil, fmt.Errorf("%s %s/%s: %w", kind.Short(), scope, name, ErrNotFound)
	}
	return it, nil
}

func (Keychain) Get(kind Kind, scope, name string) (Item, error) {
	it, err := lookupKind(kind, scope, name, true)
	if err != nil {
		return Item{}, err
	}
	return *it, nil
}

func (Keychain) Set(it Item) error {
	old, err := lookup(it.Scope, it.Name, false)
	if err != nil {
		return err
	}
	if old != nil && old.Kind != it.Kind {
		// Service+account is the keychain primary key, so a secret and a
		// variable cannot share a name within one scope.
		return fmt.Errorf("%s/%s already exists in keychain as %s", it.Scope, it.Name, old.Kind.Short())
	}
	if old != nil {
		if it.Meta.GitHubUpdatedAt.IsZero() {
			it.Meta.GitHubUpdatedAt = old.Meta.GitHubUpdatedAt
		}
		if it.Meta.Visibility == "" {
			it.Meta.Visibility, it.Meta.Repos = old.Meta.Visibility, old.Meta.Repos
		}
	}
	label := it.Scope + "/" + it.Name
	if old == nil {
		add := query(it.Scope, it.Name)
		add.SetLabel(label)
		add.SetDescription(string(it.Kind))
		add.SetComment(encodeMeta(it.Meta))
		add.SetData([]byte(it.Value))
		add.SetAccessible(keychain.AccessibleWhenUnlocked)
		err = keychain.AddItem(add)
	} else {
		upd := keychain.NewItem()
		upd.SetLabel(label)
		upd.SetComment(encodeMeta(it.Meta))
		upd.SetData([]byte(it.Value))
		err = keychain.UpdateItem(query(it.Scope, it.Name), upd)
	}
	if err != nil {
		return fmt.Errorf("save %s: %w", label, err)
	}
	return nil
}

func (Keychain) SetMeta(kind Kind, scope, name string, meta Meta) error {
	if _, err := lookupKind(kind, scope, name, false); err != nil {
		return err
	}
	upd := keychain.NewItem()
	upd.SetComment(encodeMeta(meta))
	if err := keychain.UpdateItem(query(scope, name), upd); err != nil {
		return fmt.Errorf("save metadata %s/%s: %w", scope, name, err)
	}
	return nil
}

func (Keychain) Delete(kind Kind, scope, name string) error {
	if _, err := lookupKind(kind, scope, name, false); err != nil {
		return err
	}
	if err := keychain.DeleteItem(query(scope, name)); err != nil && !errors.Is(err, keychain.ErrorItemNotFound) {
		return fmt.Errorf("delete %s/%s: %w", scope, name, err)
	}
	return nil
}
