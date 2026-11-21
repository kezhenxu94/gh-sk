package store

import (
	"fmt"
	"os"
)

// keychainStore is set when the Keychain is compiled in (macOS with cgo).
var keychainStore Store

// New returns the backend named by GH_SK_STORE; see Open.
func New() (Store, error) {
	s, err := Open(os.Getenv("GH_SK_STORE"))
	if err != nil {
		return nil, fmt.Errorf("GH_SK_STORE: %w", err)
	}
	return s, nil
}

// Open returns the named backend ("keychain" or "gopass"). An empty name is
// the default: the Keychain where available, gopass elsewhere.
func Open(name string) (Store, error) {
	if name == "" && keychainStore != nil {
		name = "keychain"
	}
	switch name {
	case "", "gopass":
		return NewGopass()
	case "keychain":
		if keychainStore == nil {
			return nil, fmt.Errorf("the Keychain is only available on macOS")
		}
		return keychainStore, nil
	default:
		return nil, fmt.Errorf("unknown backend %q: want keychain or gopass", name)
	}
}
