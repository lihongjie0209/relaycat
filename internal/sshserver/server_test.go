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
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

func TestServerFiltersClientEnvironment(t *testing.T) {
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
	if err := session.Setenv("LANG", "relaycat_TEST"); err != nil {
		t.Fatal(err)
	}
	if err := session.Setenv("PATH", "/attacker-controlled"); err != nil {
		t.Fatal(err)
	}
	output, err := session.Output(`printf '%s|%s' "$LANG" "$PATH"`)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output); got != "relaycat_TEST|"+defaultPath(currentUser(t)) {
		t.Fatalf("environment = %q", got)
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

func TestNewRejectsInvalidAuthenticationConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "authentication without keys", cfg: Config{}},
		{name: "no authentication with keys", cfg: Config{NoClientAuth: true, AuthorizedKeys: []string{"key"}}},
		{name: "empty authorized keys", cfg: Config{AuthorizedKeys: []string{"# comment only\n"}}},
		{name: "malformed authorized key", cfg: Config{AuthorizedKeys: []string{"not-a-public-key"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.cfg.HostKeyPath = filepath.Join(t.TempDir(), "host-key")
			if _, err := New(test.cfg); err == nil {
				t.Fatal("New succeeded with invalid authentication configuration")
			}
		})
	}
}

func TestPublicKeyHandlerParsesMultipleFilesAndComments(t *testing.T) {
	t.Parallel()
	first := newTestSigner(t)
	second := newTestSigner(t)
	handler, err := publicKeyHandler([]string{
		"# workstation\n\n" + string(gossh.MarshalAuthorizedKey(first.PublicKey())),
		string(gossh.MarshalAuthorizedKey(second.PublicKey())),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handler(nil, first.PublicKey()) || !handler(nil, second.PublicKey()) {
		t.Fatal("an authorized key was rejected")
	}
	if handler(nil, newTestSigner(t).PublicKey()) {
		t.Fatal("an unknown key was accepted")
	}
}

func TestAcceptEnvPair(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pair string
		want bool
	}{
		{name: "term", pair: "TERM=xterm", want: true},
		{name: "language", pair: "LANG=zh_CN.UTF-8", want: true},
		{name: "locale", pair: "LC_TIME=C", want: true},
		{name: "path", pair: "PATH=/tmp", want: false},
		{name: "shell", pair: "SHELL=/tmp/evil", want: false},
		{name: "missing separator", pair: "TERM", want: false},
		{name: "similar prefix", pair: "LANGUAGE=en", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := acceptEnvPair(test.pair); got != test.want {
				t.Fatalf("acceptEnvPair(%q) = %v, want %v", test.pair, got, test.want)
			}
		})
	}
}

func TestHostKeyRejectsMalformedAndPublicFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode os.FileMode
		data string
	}{
		{name: "malformed", mode: 0o600, data: "not a private key"},
		{name: "group readable", mode: 0o640, data: "not relevant"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.mode != 0o600 {
				t.Skip("Unix permission behavior")
			}
			path := filepath.Join(t.TempDir(), "host-key")
			if err := os.WriteFile(path, []byte(test.data), test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOrCreateHostKey(path); err == nil {
				t.Fatal("invalid host key was accepted")
			}
		})
	}
}

func TestHostKeyRejectsUnusableParentPath(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateHostKey(filepath.Join(parent, "host-key")); err == nil {
		t.Fatal("host key creation succeeded below a regular file")
	}
}

func TestDefaultHostKeyPath(t *testing.T) {
	t.Parallel()
	path, err := DefaultHostKeyPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "ssh_host_ed25519_key" || filepath.Base(filepath.Dir(path)) != "ssh" {
		t.Fatalf("unexpected default host key path %q", path)
	}
}

func TestConcurrentHostKeyCreationReturnsOneIdentity(t *testing.T) {
	t.Parallel()
	const callers = 16
	path := filepath.Join(t.TempDir(), "nested", "host-key")
	results := make(chan gossh.Signer, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			signer, err := loadOrCreateHostKey(path)
			if err != nil {
				errs <- err
				return
			}
			results <- signer
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("loadOrCreateHostKey: %v", err)
	}
	var identity []byte
	for signer := range results {
		if identity == nil {
			identity = signer.PublicKey().Marshal()
			continue
		}
		if !bytes.Equal(identity, signer.PublicKey().Marshal()) {
			t.Fatal("concurrent callers received different host keys")
		}
	}
}

func TestHandleConnStopsOnContextCancellation(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, Config{
		HostKeyPath:  filepath.Join(t.TempDir(), "host-key"),
		NoClientAuth: true,
	})
	serverConn, clientConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.HandleConn(ctx, serverConn) }()
	cancel()
	defer func() { _ = clientConn.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleConn returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HandleConn did not stop after context cancellation")
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

func currentUser(t *testing.T) *user.User {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u
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
