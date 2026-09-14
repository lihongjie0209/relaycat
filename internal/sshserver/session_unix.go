// Portions derived from github.com/tailscale/tailcat.
// Copyright (c) Tailscale Inc & contributors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux || darwin

package sshserver

import (
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	ssh "github.com/gliderlabs/ssh"
	"golang.org/x/sys/unix"
)

func runWithPTY(sess ssh.Session, cmd *exec.Cmd, request ssh.Pty, windows <-chan ssh.Window) {
	if request.Term != "" {
		cmd.Env = append(cmd.Env, "TERM="+request.Term)
	}
	size := &pty.Winsize{Rows: terminalDimension(request.Window.Height), Cols: terminalDimension(request.Window.Width)}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		exitSession(sess, err)
		return
	}
	defer func() { _ = ptmx.Close() }()
	resizeFD, err := unix.Dup(int(ptmx.Fd()))
	if err != nil {
		exitSession(sess, err)
		return
	}
	resizeFile := os.NewFile(uintptr(resizeFD), "relaycat-pty-resize")
	go func() {
		defer func() { _ = resizeFile.Close() }()
		for window := range windows {
			_ = pty.Setsize(resizeFile, &pty.Winsize{
				Rows: terminalDimension(window.Height), Cols: terminalDimension(window.Width),
			})
		}
	}()
	go func() { _, _ = io.Copy(ptmx, sess) }()
	_, _ = io.Copy(sess, ptmx)
	exitSession(sess, cmd.Wait())
}

func terminalDimension(value int) uint16 {
	const maxUint16 = int(^uint16(0))
	if value < 1 {
		return 1
	}
	if value > maxUint16 {
		return ^uint16(0)
	}
	return uint16(value) // #nosec G115 -- value is bounded above.
}
