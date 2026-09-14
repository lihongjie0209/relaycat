package cli

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestServiceInstallValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "separator required", args: []string{"service", "install", "relay"}, want: "must follow --"},
		{name: "command required", args: []string{"service", "install", "--"}, want: "command is required"},
		{name: "recursive command", args: []string{"service", "install", "--", "service", "status"}, want: "cannot run another service command"},
		{name: "invalid name", args: []string{"service", "install", "--name", `bad\\name`, "--", "relay"}, want: "service name"},
		{name: "invalid startup", args: []string{"service", "install", "--startup", "sometimes", "--", "relay"}, want: "startup must be"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			err := Execute(context.Background(), &stdout, &stderr, test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestServiceCommandsReportUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows behavior")
	}
	t.Parallel()
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), &stdout, &stderr, []string{"service", "status"})
	if err == nil || !strings.Contains(err.Error(), "only supported on Windows") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseStartup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  bool
		isErr bool
	}{
		{name: "automatic", value: "automatic", want: true},
		{name: "manual", value: "manual", want: false},
		{name: "invalid", value: "delayed", isErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseStartup(test.value)
			if got != test.want || (err != nil) != test.isErr {
				t.Fatalf("parseStartup(%q) = (%v, %v)", test.value, got, err)
			}
		})
	}
}
