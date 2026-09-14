package tunnelcrypto

import (
	"errors"
	"fmt"

	"github.com/flynn/noise"
)

type Initiator struct{ hs *noise.HandshakeState }

type Transport struct {
	send *noise.CipherState
	recv *noise.CipherState
}

func prologue(routeID, sessionID []byte) []byte {
	b := []byte("relaycat/v1")
	b = append(b, routeID...)
	return append(b, sessionID...)
}

func config(psk, routeID, sessionID []byte, initiator bool) noise.Config {
	return noise.Config{
		CipherSuite:  noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s),
		Pattern:      noise.HandshakeNN,
		Initiator:    initiator,
		PresharedKey: psk,
		Prologue:     prologue(routeID, sessionID),
	}
}

func StartInitiator(psk, routeID, sessionID []byte) ([]byte, *Initiator, error) {
	hs, err := noise.NewHandshakeState(config(psk, routeID, sessionID, true))
	if err != nil {
		return nil, nil, fmt.Errorf("creating noise initiator: %w", err)
	}
	hello, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("writing noise client hello: %w", err)
	}
	return hello, &Initiator{hs: hs}, nil
}

func (i *Initiator) Finish(agentHello []byte) (*Transport, error) {
	if i == nil || i.hs == nil {
		return nil, errors.New("noise initiator is not initialized")
	}
	_, send, recv, err := i.hs.ReadMessage(nil, agentHello)
	if err != nil {
		return nil, fmt.Errorf("verifying noise agent hello: %w", err)
	}
	i.hs = nil
	return &Transport{send: send, recv: recv}, nil
}

func Respond(psk, routeID, sessionID, clientHello []byte) ([]byte, *Transport, error) {
	hs, err := noise.NewHandshakeState(config(psk, routeID, sessionID, false))
	if err != nil {
		return nil, nil, fmt.Errorf("creating noise responder: %w", err)
	}
	if _, _, _, err := hs.ReadMessage(nil, clientHello); err != nil {
		return nil, nil, fmt.Errorf("verifying noise client hello: %w", err)
	}
	hello, recv, send, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("writing noise agent hello: %w", err)
	}
	return hello, &Transport{send: send, recv: recv}, nil
}

func (t *Transport) Encrypt(plaintext []byte) ([]byte, error) {
	if t == nil || t.send == nil {
		return nil, errors.New("noise transport is not initialized")
	}
	b, err := t.send.Encrypt(nil, nil, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypting frame: %w", err)
	}
	return b, nil
}

func (t *Transport) Decrypt(ciphertext []byte) ([]byte, error) {
	if t == nil || t.recv == nil {
		return nil, errors.New("noise transport is not initialized")
	}
	b, err := t.recv.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypting frame: %w", err)
	}
	return b, nil
}
