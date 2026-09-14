//go:build linux

package winservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain", value: "serve", want: `"serve"`},
		{name: "spaces", value: "/path with spaces/key", want: `"/path with spaces/key"`},
		{name: "quotes and slash", value: `a\b"c`, want: `"a\\b\"c"`},
		{name: "specifier", value: "value-%n", want: `"value-%%n"`},
		{name: "controls", value: "line\n\tend", want: `"line\n\tend"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := systemdQuote(test.value); got != test.want {
				t.Fatalf("systemdQuote(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	t.Parallel()
	unit, err := renderSystemdUnit(InstallConfig{
		Name:        "relaycat-ssh",
		Description: "Relaycat 100%\nSSH",
		Executable:  "/opt/relay cat/relaycat",
		Arguments:   []string{"serve", "ssh", "--state", "/var/lib/relaycat/state file.json"},
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"Description=Relaycat 100%% SSH",
		`ExecStart="/opt/relay cat/relaycat" "serve" "ssh" "--state" "/var/lib/relaycat/state file.json"`,
		"Restart=on-failure",
		"KillSignal=SIGTERM",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit does not contain %q:\n%s", want, text)
		}
	}
}

func TestRenderSystemdUnitRequiresAbsoluteExecutable(t *testing.T) {
	t.Parallel()
	if _, err := renderSystemdUnit(InstallConfig{Executable: "relaycat"}); err == nil {
		t.Fatal("relative executable was accepted")
	}
}

func TestSystemdServiceLifecycle(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "systemctl.log")
	scriptPath := filepath.Join(dir, "systemctl")
	script := `#!/bin/sh
echo "$*" >> "$RELAYCAT_SYSTEMCTL_LOG"
case "$*" in
  *ActiveState*) echo active ;;
  *MainPID*) echo 1234 ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable.
		t.Fatal(err)
	}
	oldDirectory, oldRunner := systemdUnitDirectory, runSystemctlCommand
	systemdUnitDirectory = dir
	runSystemctlCommand = func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, scriptPath, args...) // #nosec G204 -- test-controlled executable path.
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	t.Cleanup(func() {
		systemdUnitDirectory, runSystemctlCommand = oldDirectory, oldRunner
	})
	t.Setenv("RELAYCAT_SYSTEMCTL_LOG", logPath)

	cfg := InstallConfig{
		Name: "relaycat-ssh", Description: "Relaycat SSH", Executable: "/usr/local/bin/relaycat",
		Arguments: []string{"serve", "ssh", "--state", "/var/lib/relaycat/state.json"}, Automatic: true,
	}
	if err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(dir, "relaycat-ssh.service")
	info, err := os.Stat(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("unit permissions = %o, want 644", got)
	}
	if err := Start(cfg.Name); err != nil {
		t.Fatal(err)
	}
	status, err := Query(cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "active" || status.ProcessID != 1234 {
		t.Fatalf("status = %+v", status)
	}
	if err := Stop(context.Background(), cfg.Name); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(cfg.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists after uninstall: %v", err)
	}
	logData, err := os.ReadFile(logPath) // #nosec G304 -- test-controlled temporary path.
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	for _, want := range []string{
		"daemon-reload", "enable relaycat-ssh.service", "start relaycat-ssh.service",
		"stop relaycat-ssh.service", "disable --now relaycat-ssh.service",
	} {
		if !strings.Contains(logText, want) {
			t.Errorf("systemctl log does not contain %q:\n%s", want, logText)
		}
	}
}
