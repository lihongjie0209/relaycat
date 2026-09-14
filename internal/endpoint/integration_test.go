//go:build integration

package endpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/lihongjie0209/relaycat/internal/accesscode"
	"github.com/lihongjie0209/relaycat/internal/relay"
)

func TestEndToEndTCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	relayReady := make(chan net.Addr, 1)
	go func() {
		_ = relay.RunServer(ctx, relay.ServerConfig{Listen: "127.0.0.1:0", H2C: true, Token: "test-token", Logger: log}, func(a net.Addr) { relayReady <- a })
	}()
	relayAddr := <-relayReady
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go echoServer(target)
	code, err := accesscode.New("http://" + relayAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = RunAgent(ctx, AgentConfig{Code: code, Target: target.Addr().String(), Token: "test-token", AllowInsecure: true, Logger: log})
	}()
	time.Sleep(100 * time.Millisecond)
	localReady := make(chan net.Addr, 1)
	go func() {
		_ = RunClient(ctx, ClientConfig{Code: code, Listen: "127.0.0.1:0", Token: "test-token", AllowInsecure: true, Logger: log}, func(a net.Addr) { localReady <- a })
	}()
	local := <-localReady
	var conn net.Conn
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		conn, err = net.DialTimeout("tcp", local.String(), time.Second)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := []byte("hello through encrypted grpc relay")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q", got)
	}

	bulk, err := net.DialTimeout("tcp", local.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	large := bytes.Repeat([]byte("encrypted-relay-data-"), 50_000)
	if _, err := bulk.Write(large); err != nil {
		t.Fatal(err)
	}
	if err := bulk.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	returned, err := io.ReadAll(bulk)
	if err != nil {
		t.Fatal(err)
	}
	_ = bulk.Close()
	if !bytes.Equal(returned, large) {
		t.Fatalf("half-close transfer mismatch: got %d bytes, want %d", len(returned), len(large))
	}
}

func echoServer(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
	}
}

var _ = errors.Is
