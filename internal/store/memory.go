package store

import (
	"fmt"
	"sync"
	"time"
)

// merge applies the Set rules shared by all stores to it, given the existing item (or nil).
func merge(old *Item, it Item) (Item, error) {
	if old == nil {
		return it, nil
	}
	if old.Kind != it.Kind {
		// Service+account is the keychain primary key, so a secret and a
		// variable cannot share a name within one scope.
		return it, fmt.Errorf("%s/%s already exists in keychain as %s", it.Scope, it.Name, old.Kind.Short())
	}
	if it.Meta.GitHubUpdatedAt.IsZero() {
		it.Meta.GitHubUpdatedAt = old.Meta.GitHubUpdatedAt
	}
	if it.Meta.Visibility == "" {
		it.Meta.Visibility, it.Meta.Repos = old.Meta.Visibility, old.Meta.Repos
	}
	return it, nil
}

// Memory is an in-memory Store with the same semantics as Keychain, used in tests.
type Memory struct {
	mu    sync.Mutex
	items map[[2]string]Item // key: scope, name
}

func NewMemory() *Memory { return &Memory{items: map[[2]string]Item{}} }

func (m *Memory) List(kind Kind) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []Item
	for _, it := range m.items {
		if it.Kind == kind {
			it.Value = ""
			items = append(items, it)
		}
	}
	return items, nil
}

func (m *Memory) get(kind Kind, scope, name string) (Item, error) {
	it, ok := m.items[[2]string{scope, name}]
	if !ok || it.Kind != kind {
		return Item{}, fmt.Errorf("%s %s/%s: %w", kind.Short(), scope, name, ErrNotFound)
	}
	return it, nil
}

func (m *Memory) Get(kind Kind, scope, name string) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.get(kind, scope, name)
}

func (m *Memory) Set(it Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := [2]string{it.Scope, it.Name}
	var old *Item
	if o, ok := m.items[key]; ok {
		old = &o
	}
	it, err := merge(old, it)
	if err != nil {
		return err
	}
	it.Modified = time.Now()
	m.items[key] = it
	return nil
}

// SetMeta also bumps Modified, as the keychain does for any attribute change.
func (m *Memory) SetMeta(kind Kind, scope, name string, meta Meta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, err := m.get(kind, scope, name)
	if err != nil {
		return err
	}
	it.Meta, it.Modified = meta, time.Now()
	m.items[[2]string{scope, name}] = it
	return nil
}

func (m *Memory) Delete(kind Kind, scope, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(kind, scope, name); err != nil {
		return err
	}
	delete(m.items, [2]string{scope, name})
	return nil
}
