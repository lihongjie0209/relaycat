package tunnelcrypto

import (
	"fmt"
	"testing"
)

func BenchmarkNoiseHandshake(b *testing.B) {
	psk := make([]byte, 32)
	routeID := make([]byte, 16)
	sessionID := make([]byte, 16)
	b.ReportAllocs()
	for b.Loop() {
		clientHello, initiator, err := StartInitiator(psk, routeID, sessionID)
		if err != nil {
			b.Fatal(err)
		}
		agentHello, _, err := Respond(psk, routeID, sessionID, clientHello)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := initiator.Finish(agentHello); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNoiseTransport(b *testing.B) {
	for _, size := range []int{64, 1024, 32 << 10} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			initiator, responder := benchmarkTransports(b)
			payload := make([]byte, size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				ciphertext, err := initiator.Encrypt(payload)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := responder.Decrypt(ciphertext); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkTransports(b *testing.B) (*Transport, *Transport) {
	b.Helper()
	psk := make([]byte, 32)
	routeID := make([]byte, 16)
	sessionID := make([]byte, 16)
	clientHello, initiator, err := StartInitiator(psk, routeID, sessionID)
	if err != nil {
		b.Fatal(err)
	}
	agentHello, responder, err := Respond(psk, routeID, sessionID, clientHello)
	if err != nil {
		b.Fatal(err)
	}
	client, err := initiator.Finish(agentHello)
	if err != nil {
		b.Fatal(err)
	}
	return client, responder
}
