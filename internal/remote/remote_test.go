package remote

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/google/go-github/v93/github"
	"golang.org/x/crypto/nacl/box"
)

func TestSealRoundTrip(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk := &github.PublicKey{Key: github.Ptr(base64.StdEncoding.EncodeToString(pub[:]))}
	enc, err := seal(pk, "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(enc)
	got, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok || string(got) != "s3cr3t" {
		t.Fatalf("OpenAnonymous = %q, %v", got, ok)
	}
}
