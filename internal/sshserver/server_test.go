package sshserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestServerPublicKeyAuthenticationAndExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell command syntax")
	}
	t.Parallel()
	clientSigner := newTestSigner(t)
	server := newTestServer(t, Config{
		HostKeyPath:    filepath.Join(t.TempDir(), "host-key"),
		AuthorizedKeys: []string{string(gossh.MarshalAuthorizedKey(clientSigner.PublicKey()))},
	})
	client := dialTestServer(t, server, &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(clientSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // Test-only ephemeral host key.
		Timeout:         3 * time.Second,
	})
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Output("printf relaycat-ssh")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "relaycat-ssh" {
		t.Fatalf("output = %q", output)
	}
}

func TestServerRejectsUnauthorizedKey(t *testing.T) {
	t.Parallel()
	allowed := newTestSigner(t)
	other := newTestSigner(t)
	server := newTestServer(t, Config{
		HostKeyPath:    filepath.Join(t.TempDir(), "host-key"),
		AuthorizedKeys: []string{string(gossh.MarshalAuthorizedKey(allowed.PublicKey()))},
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = server.HandleConn(context.Background(), conn)
		}
	}()
	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(other)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // Test-only ephemeral host key.
		Timeout:         3 * time.Second,
	})
	if client != nil {
		_ = client.Close()
	}
	if err == nil {
		t.Fatal("unauthorized key was accepted")
	}
}

func TestNoClientAuth(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, Config{
		HostKeyPath:  filepath.Join(t.TempDir(), "host-key"),
		NoClientAuth: true,
	})
	client := dialTestServer(t, server, &gossh.ClientConfig{
		User:            "test",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // Test-only ephemeral host key.
		Timeout:         3 * time.Second,
	})
	_ = client.Close()
}

func TestServerPTYAndExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY behavior")
	}
	clientSigner := newTestSigner(t)
	server := newTestServer(t, Config{
		HostKeyPath:    filepath.Join(t.TempDir(), "host-key"),
		AuthorizedKeys: []string{string(gossh.MarshalAuthorizedKey(clientSigner.PublicKey()))},
	})
	client := dialTestServer(t, server, &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(clientSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // Test-only ephemeral host key.
		Timeout:         3 * time.Second,
	})
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	output, err := session.CombinedOutput(`printf "$TERM"; exit 7`)
	if string(output) != "xterm-256color" {
		t.Fatalf("PTY output = %q", output)
	}
	var exitErr *gossh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 7 {
		t.Fatalf("exit error = %v, want SSH exit status 7", err)
	}
}

func TestHostKeyIsPersistentAndPrivate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "host-key")
	first, err := loadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Fatal("host key changed after reload")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("host key mode = %o, want 600", got)
	}
}

func TestAuthorizedKeyOptionsAreRejected(t *testing.T) {
	t.Parallel()
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(newTestSigner(t).PublicKey())))
	if _, err := publicKeyHandler([]string{`command="false" ` + line}); err == nil {
		t.Fatal("authorized_keys options were accepted")
	}
}

func newTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func newTestSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func dialTestServer(t *testing.T, server *Server, cfg *gossh.ClientConfig) *gossh.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = server.HandleConn(context.Background(), conn)
		}
	}()
	client, err := gossh.Dial("tcp", listener.Addr().String(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
