//go:build windows

package sshserver

import (
	"os/exec"

	ssh "github.com/gliderlabs/ssh"
)

func runWithPTY(sess ssh.Session, cmd *exec.Cmd, _ ssh.Pty, _ <-chan ssh.Window) {
	runWithPipes(sess, cmd)
}
