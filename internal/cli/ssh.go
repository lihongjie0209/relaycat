package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/lihongjie0209/relaycat/internal/accesscode"
	"github.com/lihongjie0209/relaycat/internal/endpoint"
	"github.com/spf13/cobra"
)

type sshOptions struct {
	codeFile       string
	user           string
	identityFile   string
	tokenFile      string
	caFile         string
	sshOptions     []string
	allowInsecure  bool
	noAuth         bool
	acceptNewHost  bool
	idleTimeout    time.Duration
	connectionCode string
	remoteCommand  []string
}

func newSSHCommand(ctx context.Context, root *rootOptions) *cobra.Command {
	opts := sshOptions{}
	cmd := &cobra.Command{
		Use:   "ssh [[user@]connection-code] [command [args...]]",
		Short: "connect with OpenSSH through a temporary Relaycat tunnel",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.resolveInput(args); err != nil {
				return err
			}
			code, err := accesscode.Decode(opts.connectionCode, opts.allowInsecure)
			if err != nil {
				return err
			}
			token, err := endpoint.ReadToken(opts.tokenFile, "RELAYCAT_TOKEN")
			if err != nil {
				return err
			}
			log, err := logger(cmd.ErrOrStderr(), root, false)
			if err != nil {
				return err
			}
			clientCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			ready := make(chan net.Addr, 1)
			clientDone := make(chan error, 1)
			go func() {
				clientDone <- endpoint.RunClient(clientCtx, endpoint.ClientConfig{
					Code: code, Listen: "127.0.0.1:0", Token: token,
					CAFile: opts.caFile, AllowInsecure: opts.allowInsecure,
					IdleTimeout: opts.idleTimeout, Logger: log,
				}, func(addr net.Addr) { ready <- addr })
			}()

			var addr net.Addr
			select {
			case addr = <-ready:
			case err := <-clientDone:
				return fmt.Errorf("starting temporary tunnel: %w", err)
			case <-ctx.Done():
				return ctx.Err()
			}
			sshErr := runOpenSSH(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), addr, code, opts)
			cancel()
			clientErr := <-clientDone
			if sshErr != nil {
				return sshErr
			}
			if clientErr != nil && !errors.Is(clientErr, context.Canceled) {
				return fmt.Errorf("closing temporary tunnel: %w", clientErr)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.codeFile, "code-file", "", "read the connection code from a protected file")
	f.StringVarP(&opts.user, "user", "l", "relaycat", "SSH username")
	f.StringVarP(&opts.identityFile, "identity-file", "i", "", "SSH private key file")
	f.StringSliceVarP(&opts.sshOptions, "ssh-option", "o", nil, "OpenSSH option; may be repeated")
	f.BoolVar(&opts.noAuth, "no-auth", false, "disable SSH public-key and password authentication")
	f.BoolVar(&opts.acceptNewHost, "accept-new-host-key", false, "accept a new host key but reject a changed key")
	f.StringVar(&opts.tokenFile, "token-file", "", "Relay bearer token file")
	f.StringVar(&opts.caFile, "ca-file", "", "additional CA certificate file")
	f.BoolVar(&opts.allowInsecure, "allow-insecure-relay", false, "allow a cleartext http Relay URL")
	f.DurationVar(&opts.idleTimeout, "idle-timeout", 30*time.Minute, "close the tunnel after this much TCP inactivity")
	return cmd
}

func (o *sshOptions) resolveInput(args []string) error {
	o.remoteCommand = args
	if len(args) > 0 {
		candidate := args[0]
		if at := strings.Index(candidate, "@"+accesscode.Prefix); at >= 0 {
			if o.user != "relaycat" {
				return errors.New("username cannot be specified both before the connection code and with --user")
			}
			o.user = candidate[:at]
			candidate = candidate[at+1:]
		}
		if strings.HasPrefix(candidate, accesscode.Prefix) {
			o.connectionCode = candidate
			o.remoteCommand = args[1:]
		}
	}
	if o.connectionCode != "" && o.codeFile != "" {
		return errors.New("connection code cannot be specified both as an argument and with --code-file")
	}
	if o.connectionCode == "" && o.codeFile != "" {
		path := expandHome(o.codeFile)
		if err := accesscode.CheckPrivateFile(path); err != nil {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 -- explicitly selected connection-code file.
		if err != nil {
			return fmt.Errorf("reading connection code file: %w", err)
		}
		o.connectionCode = strings.TrimSpace(string(data))
	}
	if o.connectionCode == "" {
		o.connectionCode = strings.TrimSpace(os.Getenv("RELAYCAT_CODE"))
	}
	if o.connectionCode == "" {
		return errors.New("connection code is required as an argument, --code-file, or RELAYCAT_CODE")
	}
	if !validSSHUser(o.user) {
		return errors.New("SSH username contains unsupported characters")
	}
	return nil
}

func runOpenSSH(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, addr net.Addr, code accesscode.Code, opts sshOptions) error {
	args, err := buildOpenSSHArgs(addr, code, opts)
	if err != nil {
		return err
	}

	// #nosec G204 -- the executable is fixed; arguments are passed directly without a shell.
	ssh := exec.CommandContext(ctx, "ssh", args...)
	ssh.Stdin = stdin
	ssh.Stdout = stdout
	ssh.Stderr = stderr
	ssh.Env = withoutEnvironment(os.Environ(), "RELAYCAT_CODE", "RELAYCAT_TOKEN")
	if err := ssh.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("ssh exited with status %s: %w", strconv.Itoa(exitErr.ExitCode()), err)
		}
		return fmt.Errorf("running OpenSSH: %w", err)
	}
	return nil
}

func buildOpenSSHArgs(addr net.Addr, code accesscode.Code, opts sshOptions) ([]string, error) {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, fmt.Errorf("reading temporary tunnel address: %w", err)
	}
	args := []string{"-p", port, "-o", "HostKeyAlias=relaycat-" + hex.EncodeToString(code.RouteID)}
	if opts.identityFile != "" {
		args = append(args, "-i", expandHome(opts.identityFile))
	}
	if opts.noAuth {
		args = append(args, "-o", "PubkeyAuthentication=no", "-o", "PasswordAuthentication=no")
	}
	if opts.acceptNewHost {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	for _, option := range opts.sshOptions {
		args = append(args, "-o", option)
	}
	args = append(args, opts.user+"@127.0.0.1")
	args = append(args, opts.remoteCommand...)
	return args, nil
}

func validSSHUser(user string) bool {
	if user == "" {
		return false
	}
	for index, r := range user {
		if index == 0 && !validSSHUserStart(r) {
			return false
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			continue
		}
		return false
	}
	return true
}

func validSSHUserStart(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
}

func withoutEnvironment(environment []string, names ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		remove := false
		for _, sensitive := range names {
			if strings.EqualFold(name, sensitive) {
				remove = true
				break
			}
		}
		if !remove {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
