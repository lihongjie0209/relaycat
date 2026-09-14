// Portions derived from github.com/tailscale/tailcat.
// Copyright (c) Tailscale Inc & contributors.
// SPDX-License-Identifier: BSD-3-Clause

package sshserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	gossh "golang.org/x/crypto/ssh"
)

func loadOrCreateHostKey(path string) (gossh.Signer, error) {
	if path == "" {
		var err error
		path, err = DefaultHostKeyPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating SSH key directory: %w", err)
	}
	key, err := readPrivateKey(path)
	if err == nil {
		return gossh.ParsePrivateKey(key)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating SSH host key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encoding SSH host key: %w", err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator-selected host key path.
	if errors.Is(err, os.ErrExist) {
		key, err = readPrivateKey(path)
		if err != nil {
			return nil, err
		}
		return gossh.ParsePrivateKey(key)
	}
	if err != nil {
		return nil, fmt.Errorf("creating SSH host key: %w", err)
	}
	if _, err := f.Write(pemData); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("writing SSH host key: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("syncing SSH host key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("closing SSH host key: %w", err)
	}
	return gossh.ParsePrivateKey(pemData)
}

func readPrivateKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("SSH host key %s is accessible by group or other users", path)
	}
	key, err := os.ReadFile(path) // #nosec G304 -- operator-selected host key path.
	if err != nil {
		return nil, fmt.Errorf("reading SSH host key: %w", err)
	}
	return key, nil
}
