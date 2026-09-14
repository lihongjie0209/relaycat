package cli

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lihongjie0209/relaycat/internal/accesscode"
)

func TestSSHOptionsResolveInput(t *testing.T) {
	t.Setenv("RELAYCAT_CODE", "")
	tests := []struct {
		name        string
		opts        sshOptions
		args        []string
		wantUser    string
		wantCommand []string
		wantErr     string
	}{
		{name: "argument", opts: sshOptions{user: "relaycat"}, args: []string{"rc1_example", "hostname"}, wantUser: "relaycat", wantCommand: []string{"hostname"}},
		{name: "embedded user", opts: sshOptions{user: "relaycat"}, args: []string{"admin@rc1_example", "whoami"}, wantUser: "admin", wantCommand: []string{"whoami"}},
		{name: "missing", opts: sshOptions{user: "relaycat"}, wantErr: "connection code is required"},
		{name: "invalid user", opts: sshOptions{user: "-bad"}, args: []string{"rc1_example"}, wantErr: "username"},
		{name: "duplicate user", opts: sshOptions{user: "admin"}, args: []string{"other@rc1_example"}, wantErr: "both"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := test.opts
			err := opts.resolveInput(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.user != test.wantUser || !reflect.DeepEqual(opts.remoteCommand, test.wantCommand) {
				t.Fatalf("resolved user %q command %v", opts.user, opts.remoteCommand)
			}
		})
	}
}

func TestSSHOptionsReadPrivateCodeFile(t *testing.T) {
	t.Setenv("RELAYCAT_CODE", "")
	path := filepath.Join(t.TempDir(), "code")
	if err := os.WriteFile(path, []byte("rc1_from_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := sshOptions{user: "relaycat", codeFile: path}
	if err := opts.resolveInput([]string{"hostname"}); err != nil {
		t.Fatal(err)
	}
	if opts.connectionCode != "rc1_from_file" || !reflect.DeepEqual(opts.remoteCommand, []string{"hostname"}) {
		t.Fatalf("resolved code %q command %v", opts.connectionCode, opts.remoteCommand)
	}
}

func TestSSHOptionsUseEnvironmentWithoutTreatingCommandAsCode(t *testing.T) {
	t.Setenv("RELAYCAT_CODE", "rc1_from_environment")
	opts := sshOptions{user: "relaycat"}
	if err := opts.resolveInput([]string{"hostname", "--fqdn"}); err != nil {
		t.Fatal(err)
	}
	if opts.connectionCode != "rc1_from_environment" || !reflect.DeepEqual(opts.remoteCommand, []string{"hostname", "--fqdn"}) {
		t.Fatalf("resolved code %q command %v", opts.connectionCode, opts.remoteCommand)
	}
}

func TestValidSSHUser(t *testing.T) {
	t.Parallel()
	for _, user := range []string{"relaycat", "domain.user", "service-account", "user_1"} {
		if !validSSHUser(user) {
			t.Errorf("valid user %q rejected", user)
		}
	}
	for _, user := range []string{"", "root@host", "-oProxyCommand=bad", "user name"} {
		if validSSHUser(user) {
			t.Errorf("invalid user %q accepted", user)
		}
	}
}

func TestBuildOpenSSHArgs(t *testing.T) {
	t.Parallel()
	args, err := buildOpenSSHArgs(
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2345},
		accesscode.Code{RouteID: []byte{0x01, 0x02}},
		sshOptions{
			user: "admin", identityFile: "/tmp/id", noAuth: true, acceptNewHost: true,
			sshOptions: []string{"ConnectTimeout=5"}, remoteCommand: []string{"hostname"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-p", "2345", "-o", "HostKeyAlias=relaycat-0102", "-i", "/tmp/id",
		"-o", "PubkeyAuthentication=no", "-o", "PasswordAuthentication=no",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=5",
		"admin@127.0.0.1", "hostname",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestWithoutEnvironmentRemovesSecrets(t *testing.T) {
	t.Parallel()
	got := withoutEnvironment([]string{
		"PATH=/usr/bin", "RELAYCAT_CODE=secret-code", "relaycat_token=secret-token", "LANG=C",
	}, "RELAYCAT_CODE", "RELAYCAT_TOKEN")
	want := []string{"PATH=/usr/bin", "LANG=C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
}
