package accesscode

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCodeRoundTrip(t *testing.T) {
	t.Parallel()
	want, err := New("https://relay.example.com:8443")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(s, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.RelayURL != want.RelayURL || !bytes.Equal(got.RouteID, want.RouteID) || !bytes.Equal(got.PSK, want.PSK) {
		t.Fatalf("round trip mismatch: %#v != %#v", got, want)
	}
}

func TestLoadOrCreate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := LoadOrCreate(path, "https://relay.example.com", "127.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path, "https://relay.example.com", "127.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.RouteID, second.RouteID) || !bytes.Equal(first.PSK, second.PSK) {
		t.Fatal("state was not reused")
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("state mode: %v, %v", st, err)
	}
	if _, err := LoadOrCreate(path, "https://relay.example.com", "127.0.0.1:23"); err == nil {
		t.Fatal("target mismatch succeeded")
	}
}

func FuzzDecode(f *testing.F) {
	c, _ := New("https://relay.example.com")
	seed, _ := Encode(c)
	f.Add(seed)
	f.Add("rc1_")
	f.Add("junk")
	f.Fuzz(func(t *testing.T, s string) { _, _ = Decode(s, true) })
}
