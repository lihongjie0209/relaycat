package accesscode

import "testing"

func BenchmarkConnectionCode(b *testing.B) {
	code := Code{
		RelayURL: "https://relay.example.com:8443",
		RouteID:  make([]byte, RouteIDSize),
		PSK:      make([]byte, PSKSize),
	}
	encoded, err := Encode(code)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("encode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := Encode(code); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := Decode(encoded, false); err != nil {
				b.Fatal(err)
			}
		}
	})
}
