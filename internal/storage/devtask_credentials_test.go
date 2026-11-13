package storage

import (
	"bytes"
	"os"
	"testing"
)

func testProviderKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// The sealed value round-trips and is bound to the provider id (AAD).
func TestCredentialsRoundTripAndAAD(t *testing.T) {
	key := testProviderKey()
	sealed, err := SealProviderSecret(key, "global", []byte(`{"base_url":"https://x.example/v1","api_key":"sk-test-VALUE"}`))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := OpenProviderSecret(key, "global", sealed)
	if err != nil || !bytes.Contains(plain, []byte("sk-test-VALUE")) {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := OpenProviderSecret(key, "other", sealed); err == nil {
		t.Fatal("sealed value opened under a different provider id")
	}
	wrong := testProviderKey()
	wrong[0] ^= 0xff
	if _, err := OpenProviderSecret(wrong, "global", sealed); err == nil {
		t.Fatal("sealed value opened with a wrong key")
	}
}

// Secret material is never stored as plaintext in the database file.
func TestCredentialsNoPlaintextAtRest(t *testing.T) {
	s, path := openDevTaskStore(t)
	key := testProviderKey()
	if err := s.UpsertDevProviderMeta("global", "Global", true); err != nil {
		t.Fatal(err)
	}
	sealed, err := SealProviderSecret(key, "global", []byte(`{"base_url":"https://gw.example/v1","api_key":"sk-plain-CANARY-123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDevProviderSecret("global", sealed); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"sk-plain-CANARY-123", "gw.example"} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Fatalf("plaintext %q found in the database file", needle)
		}
	}
}

// Writing only metadata keeps an existing sealed secret; clearing removes it.
func TestProviderMetaKeepsSecretAndClearRemovesIt(t *testing.T) {
	s, _ := openDevTaskStore(t)
	key := testProviderKey()
	if err := s.UpsertDevProviderMeta("global", "Global", true); err != nil {
		t.Fatal(err)
	}
	sealed, _ := SealProviderSecret(key, "global", []byte("payload"))
	if err := s.SetDevProviderSecret("global", sealed); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDevProviderMeta("global", "Renamed", false); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetDevProvider("global")
	if err != nil || !p.KeySet || p.DisplayName != "Renamed" || p.Enabled || !bytes.Equal(p.Sealed(), sealed) {
		t.Fatalf("meta update changed the secret: %+v %v", p, err)
	}
	if err := s.SetDevProviderSecret("global", nil); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetDevProvider("global")
	if p.KeySet || len(p.Sealed()) != 0 {
		t.Fatalf("clear did not remove the secret: %+v", p)
	}
}
