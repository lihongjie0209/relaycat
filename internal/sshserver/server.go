// Portions derived from github.com/tailscale/tailcat.
// Copyright (c) Tailscale Inc & contributors.
// SPDX-License-Identifier: BSD-3-Clause

package sshserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	ssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const interactiveMOTD = "Connected via Relaycat SSH.\r\n"

type Config struct {
	HostKeyPath    string
	AuthorizedKeys []string
	NoClientAuth   bool
	Logger         *slog.Logger
}

type Server struct {
	server *ssh.Server
}

func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.NoClientAuth && cfg.AuthorizedKeys != nil {
		return nil, errors.New("authorized keys cannot be combined with no client authentication")
	}
	if !cfg.NoClientAuth && cfg.AuthorizedKeys == nil {
		return nil, errors.New("at least one authorized key is required")
	}
	signer, err := loadOrCreateHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, err
	}
	publicKeyHandler, err := publicKeyHandler(cfg.AuthorizedKeys)
	if err != nil {
		return nil, err
	}
	s := &Server{}
	s.server = &ssh.Server{
		Handler:           s.sessionHandler,
		HostSigners:       []ssh.Signer{signer},
		PublicKeyHandler:  publicKeyHandler,
		ChannelHandlers:   map[string]ssh.ChannelHandler{"session": ssh.DefaultSessionHandler},
		RequestHandlers:   map[string]ssh.RequestHandler{},
		SubsystemHandlers: map[string]ssh.SubsystemHandler{},
		ConnectionFailedCallback: func(_ net.Conn, err error) {
			cfg.Logger.Debug("SSH handshake failed", "error", err)
		},
	}
	return s, nil
}

func DefaultHostKeyPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config directory: %w", err)
	}
	return filepath.Join(dir, "relaycat", "ssh", "ssh_host_ed25519_key"), nil
}

func (s *Server) HandleConn(ctx context.Context, conn net.Conn) error {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	s.server.HandleConn(conn)
	return nil
}

func (s *Server) sessionHandler(sess ssh.Session) {
	u, err := user.Current()
	if err != nil {
		_, _ = fmt.Fprintf(sess.Stderr(), "failed to determine current user: %v\r\n", err)
		_ = sess.Exit(1)
		return
	}
	cmd := sessionCommand(sess.Context(), u, sess.RawCommand())
	for _, env := range sess.Environ() {
		if acceptEnvPair(env) {
			cmd.Env = append(cmd.Env, env)
		}
	}
	ptyReq, winCh, hasPTY := sess.Pty()
	if hasPTY && sess.RawCommand() == "" {
		_, _ = fmt.Fprint(sess, interactiveMOTD)
	}
	if hasPTY && runtime.GOOS != "windows" {
		runWithPTY(sess, cmd, ptyReq, winCh)
		return
	}
	runWithPipes(sess, cmd)
}

func sessionCommand(ctx context.Context, u *user.User, rawCommand string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = os.Getenv("COMSPEC")
		} else {
			shell = "/bin/sh"
		}
	}
	var args []string
	if runtime.GOOS == "windows" {
		if rawCommand == "" {
			args = []string{shell}
		} else {
			args = []string{shell, "/c", rawCommand}
		}
	} else if rawCommand == "" {
		args = []string{shell, "-l"}
	} else {
		args = []string{shell, "-c", rawCommand}
	}
	// #nosec G204,G702 -- executing the authenticated SSH client's requested command is the feature.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = u.HomeDir
	cmd.Env = []string{"HOME=" + u.HomeDir, "USER=" + u.Username, "SHELL=" + shell, "PATH=" + defaultPath(u)}
	return cmd
}

func defaultPath(u *user.User) string {
	if u.Uid == "0" {
		return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

func runWithPipes(sess ssh.Session, cmd *exec.Cmd) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		exitSession(sess, err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		exitSession(sess, err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		exitSession(sess, err)
		return
	}
	if err := cmd.Start(); err != nil {
		exitSession(sess, err)
		return
	}
	go func() { _, _ = io.Copy(stdin, sess); _ = stdin.Close() }()
	done := make(chan struct{})
	var streams atomic.Int32
	streams.Store(2)
	copyOutput := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		if streams.Add(-1) == 0 {
			close(done)
		}
	}
	go copyOutput(sess, stdout)
	go copyOutput(sess.Stderr(), stderr)
	<-done
	exitSession(sess, cmd.Wait())
}

func exitSession(sess ssh.Session, err error) {
	if err == nil {
		_ = sess.Exit(0)
		return
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		_ = sess.Exit(exitErr.ExitCode())
		return
	}
	_, _ = fmt.Fprintf(sess.Stderr(), "%v\r\n", err)
	_ = sess.Exit(1)
}

func acceptEnvPair(pair string) bool {
	key, _, ok := strings.Cut(pair, "=")
	return ok && (key == "TERM" || key == "LANG" || strings.HasPrefix(key, "LC_"))
}

func publicKeyHandler(texts []string) (ssh.PublicKeyHandler, error) {
	if texts == nil {
		return nil, nil
	}
	allowed := make(map[string]bool)
	for textIndex, text := range texts {
		for lineIndex, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, _, options, rest, err := gossh.ParseAuthorizedKey([]byte(line))
			if err != nil || len(bytes.TrimSpace(rest)) != 0 {
				if err == nil {
					err = errors.New("unexpected trailing data")
				}
				return nil, fmt.Errorf("authorized keys entry %d, line %d: %w", textIndex+1, lineIndex+1, err)
			}
			if len(options) != 0 {
				return nil, fmt.Errorf("authorized keys entry %d, line %d: options are not supported", textIndex+1, lineIndex+1)
			}
			allowed[string(key.Marshal())] = true
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("no SSH public keys found")
	}
	return func(_ ssh.Context, key ssh.PublicKey) bool {
		return allowed[string(key.Marshal())]
	}, nil
}
