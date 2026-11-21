package store

import (
	"fmt"
	"os"
)

// keychainStore is set when the Keychain is compiled in (macOS with cgo).
var keychainStore Store

// New returns the backend named by GH_SK_STORE ("keychain" or "gopass"),
// defaulting to the Keychain where available and gopass elsewhere.
func New() (Store, error) {
	b := os.Getenv("GH_SK_STORE")
	if b == "" && keychainStore != nil {
		b = "keychain"
	}
	switch b {
	case "", "gopass":
		return NewGopass()
	case "keychain":
		if keychainStore == nil {
			return nil, fmt.Errorf("GH_SK_STORE=keychain: the Keychain is only available on macOS")
		}
		return keychainStore, nil
	default:
		return nil, fmt.Errorf("GH_SK_STORE=%s: want keychain or gopass", b)
	}
}
