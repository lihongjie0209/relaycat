package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestVersion(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if err := Execute(context.Background(), &out, &errOut, []string{"version"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "relaycat") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestConnectRejectsBadCode(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	err := Execute(context.Background(), &out, &errOut, []string{"connect", "not-a-code"})
	if err == nil {
		t.Fatal("invalid code succeeded")
	}
}

func TestLoadConfigAppliesOnlyUnchangedFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("idle-timeout: 45s\nonce: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var idleTimeout time.Duration
	var once bool
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", time.Minute, "")
	cmd.Flags().BoolVar(&once, "once", false, "")
	if err := cmd.Flags().Set("idle-timeout", "2m"); err != nil {
		t.Fatal(err)
	}

	if err := loadConfig(cmd, &rootOptions{config: path}); err != nil {
		t.Fatal(err)
	}
	if idleTimeout != 2*time.Minute {
		t.Fatalf("explicit idle timeout overwritten: %s", idleTimeout)
	}
	if !once {
		t.Fatal("boolean configuration was not applied")
	}
}

func TestServeSSHRequiresAuthorizedKeys(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	err := Execute(context.Background(), &out, &errOut, []string{
		"serve", "ssh", "--relay", "http://127.0.0.1:1", "--allow-insecure-relay",
	})
	if err == nil || !strings.Contains(err.Error(), "authorized-keys-file") {
		t.Fatalf("error = %v", err)
	}
}
