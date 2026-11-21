package store

import "testing"

func TestNewErrors(t *testing.T) {
	t.Setenv("GH_SK_STORE", "vault")
	if _, err := New(); err == nil {
		t.Error("unknown GH_SK_STORE should fail")
	}
	if keychainStore == nil {
		t.Setenv("GH_SK_STORE", "keychain")
		if _, err := New(); err == nil {
			t.Error("GH_SK_STORE=keychain without the Keychain should fail")
		}
	}
}
