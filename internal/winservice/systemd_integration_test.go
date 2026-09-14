//go:build integration && linux

package winservice

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestSystemdLifecycleInContainer(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	binary := filepath.Join(t.TempDir(), "relaycat")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, "../../cmd/relaycat")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building relaycat: %v: %s", err, output)
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "trfore/docker-ubuntu2404-systemd:latest",
			Cmd:   []string{"/sbin/init"},
			HostConfigModifier: func(config *containertypes.HostConfig) {
				config.Privileged = true
				config.CgroupnsMode = "host"
				config.Binds = []string{"/sys/fs/cgroup:/sys/fs/cgroup:rw"}
				config.Tmpfs = map[string]string{"/run": "rw", "/run/lock": "rw", "/tmp": "rw"}
			},
			WaitingFor: wait.ForExec([]string{"systemctl", "show", "--property=SystemState", "--value"}).
				WithStartupTimeout(45 * time.Second),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	}
	if err := container.CopyFileToContainer(ctx, binary, "/usr/local/bin/relaycat", 0o755); err != nil {
		t.Fatal(err)
	}

	execOK(t, ctx, container,
		"/usr/local/bin/relaycat", "service", "install", "--name", "relaycat-test", "--",
		"relay", "--listen", "127.0.0.1:18080", "--h2c",
	)
	if got := strings.TrimSpace(execOK(t, ctx, container, "systemctl", "is-enabled", "relaycat-test.service")); got != "enabled" {
		t.Fatalf("is-enabled = %q", got)
	}
	execOK(t, ctx, container, "/usr/local/bin/relaycat", "service", "start", "--name", "relaycat-test")
	if got := strings.TrimSpace(execOK(t, ctx, container, "systemctl", "is-active", "relaycat-test.service")); got != "active" {
		t.Fatalf("is-active = %q", got)
	}
	if got := strings.TrimSpace(execOK(t, ctx, container, "/usr/local/bin/relaycat", "service", "status", "--name", "relaycat-test")); got != "active" {
		t.Fatalf("relaycat service status = %q", got)
	}
	execOK(t, ctx, container, "/usr/local/bin/relaycat", "service", "stop", "--name", "relaycat-test")
	code, inactiveOutput := containerExec(t, ctx, container, "systemctl", "is-active", "relaycat-test.service")
	if got := strings.TrimSpace(inactiveOutput); code != 3 || got != "inactive" {
		t.Fatalf("state after stop: exit=%d output=%q", code, got)
	}
	execOK(t, ctx, container, "/usr/local/bin/relaycat", "service", "uninstall", "--name", "relaycat-test")
	code, output := containerExec(t, ctx, container, "test", "!", "-e", "/etc/systemd/system/relaycat-test.service")
	if code != 0 {
		t.Fatalf("unit remains after uninstall: exit=%d output=%s", code, output)
	}
}

func execOK(t *testing.T, ctx context.Context, container testcontainers.Container, command ...string) string {
	t.Helper()
	code, output := containerExec(t, ctx, container, command...)
	if code != 0 {
		t.Fatalf("%s: exit=%d output=%s", strings.Join(command, " "), code, output)
	}
	return output
}

func containerExec(t *testing.T, ctx context.Context, container testcontainers.Container, command ...string) (int, string) {
	t.Helper()
	code, reader, err := container.Exec(ctx, command, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("executing %s: %v", strings.Join(command, " "), err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading %s output: %v", strings.Join(command, " "), err)
	}
	return code, string(output)
}
