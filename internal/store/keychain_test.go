//go:build darwin && cgo

package store

import (
	"os"
	"strings"
	"testing"
	"time"
)

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
