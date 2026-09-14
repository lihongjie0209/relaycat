//go:build linux

package winservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	systemdUnitDirectory = "/etc/systemd/system"
	runSystemctlCommand  = executeSystemctl
)

func Install(cfg InstallConfig) error {
	unitPath := filepath.Join(systemdUnitDirectory, cfg.Name+".service")
	if _, err := os.Lstat(unitPath); err == nil {
		return fmt.Errorf("systemd service %q already exists", cfg.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking systemd service %q: %w", cfg.Name, err)
	}
	unit, err := renderSystemdUnit(cfg)
	if err != nil {
		return err
	}
	if err := publishUnitFile(unitPath, unit); err != nil {
		return err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = os.Remove(unitPath)
			_ = systemctl(context.Background(), "daemon-reload")
		}
	}()
	if err := systemctl(context.Background(), "daemon-reload"); err != nil {
		return err
	}
	if cfg.Automatic {
		if err := systemctl(context.Background(), "enable", cfg.Name+".service"); err != nil {
			return err
		}
	}
	rollback = false
	return nil
}

func Uninstall(name string) error {
	unit := name + ".service"
	if err := systemctl(context.Background(), "disable", "--now", unit); err != nil {
		return err
	}
	path := filepath.Join(systemdUnitDirectory, unit)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing systemd unit %q: %w", path, err)
	}
	return systemctl(context.Background(), "daemon-reload")
}

func Start(name string) error {
	return systemctl(context.Background(), "start", name+".service")
}

func Stop(ctx context.Context, name string) error {
	return systemctl(ctx, "stop", name+".service")
}

func Query(name string) (Status, error) {
	state, err := systemctlOutput(context.Background(), "show", "--property=ActiveState", "--value", name+".service")
	if err != nil {
		return Status{}, err
	}
	pidText, err := systemctlOutput(context.Background(), "show", "--property=MainPID", "--value", name+".service")
	if err != nil {
		return Status{}, err
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(pidText), 10, 32)
	if err != nil {
		return Status{}, fmt.Errorf("parsing systemd MainPID %q: %w", strings.TrimSpace(pidText), err)
	}
	return Status{State: strings.TrimSpace(state), ProcessID: uint32(pid)}, nil
}

func Run(string, RunFunc) error {
	return errors.New("service run is reserved for the Windows Service Control Manager")
}

func renderSystemdUnit(cfg InstallConfig) ([]byte, error) {
	if cfg.Executable == "" || !filepath.IsAbs(cfg.Executable) {
		return nil, errors.New("systemd service executable must be an absolute path")
	}
	description := strings.NewReplacer("\r", " ", "\n", " ", "%", "%%").Replace(cfg.Description)
	var command strings.Builder
	command.WriteString(systemdQuote(cfg.Executable))
	for _, argument := range cfg.Arguments {
		command.WriteByte(' ')
		command.WriteString(systemdQuote(argument))
	}
	unit := fmt.Sprintf(`[Unit]
Description=%s
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5s
KillSignal=SIGTERM
TimeoutStopSec=30s

[Install]
WantedBy=multi-user.target
`, description, command.String())
	return []byte(unit), nil
}

func systemdQuote(value string) string {
	var result strings.Builder
	result.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\', '"':
			result.WriteByte('\\')
			result.WriteRune(r)
		case '%':
			result.WriteString("%%")
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				_, _ = fmt.Fprintf(&result, `\x%02x`, r)
			} else {
				result.WriteRune(r)
			}
		}
	}
	result.WriteByte('"')
	return result.String()
}

func publishUnitFile(path string, content []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".relaycat-unit-*")
	if err != nil {
		return fmt.Errorf("creating temporary systemd unit: %w", err)
	}
	temporaryPath := f.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return fmt.Errorf("setting systemd unit permissions: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing systemd unit: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("syncing systemd unit: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing systemd unit: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("publishing systemd unit %q: %w", path, err)
	}
	return nil
}

func systemctl(ctx context.Context, args ...string) error {
	_, err := systemctlOutput(ctx, args...)
	return err
}

func systemctlOutput(ctx context.Context, args ...string) (string, error) {
	return runSystemctlCommand(ctx, args...)
}

func executeSystemctl(ctx context.Context, args ...string) (string, error) {
	// #nosec G204 -- executable is fixed and every argument is passed without a shell.
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}
