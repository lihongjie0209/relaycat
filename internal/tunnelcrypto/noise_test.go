package tunnelcrypto

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	psk := bytes.Repeat([]byte{1}, 32)
	route := bytes.Repeat([]byte{2}, 16)
	session := bytes.Repeat([]byte{3}, 16)
	clientHello, initiator, err := StartInitiator(psk, route, session)
	if err != nil {
		t.Fatal(err)
	}
	agentHello, agent, err := Respond(psk, route, session, clientHello)
	if err != nil {
		t.Fatal(err)
	}
	client, err := initiator.Finish(agentHello)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := client.Encrypt([]byte("secret payload"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("secret payload")) {
		t.Fatal("ciphertext contains plaintext")
	}
	plaintext, err := agent.Decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "secret payload" {
		t.Fatalf("got %q", plaintext)
	}
	reply, _ := agent.Encrypt([]byte("reply"))
	got, err := client.Decrypt(reply)
	if err != nil || string(got) != "reply" {
		t.Fatalf("reply = %q, %v", got, err)
	}
}

func TestWrongPSKRejected(t *testing.T) {
	t.Parallel()
	route := bytes.Repeat([]byte{2}, 16)
	session := bytes.Repeat([]byte{3}, 16)
	hello, _, err := StartInitiator(bytes.Repeat([]byte{1}, 32), route, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Respond(bytes.Repeat([]byte{9}, 32), route, session, hello); err == nil {
		t.Fatal("wrong PSK accepted")
	}
}

func TestSessionBinding(t *testing.T) {
	t.Parallel()
	psk := bytes.Repeat([]byte{1}, 32)
	route := bytes.Repeat([]byte{2}, 16)
	hello, _, err := StartInitiator(psk, route, bytes.Repeat([]byte{3}, 16))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Respond(psk, route, bytes.Repeat([]byte{4}, 16), hello); err == nil {
		t.Fatal("cross-session handshake accepted")
	}
}
