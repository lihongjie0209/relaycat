//go:build linux || darwin

package sshserver

import "testing"

func TestTerminalDimension(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value int
		want  uint16
	}{
		{name: "negative", value: -1, want: 1},
		{name: "zero", value: 0, want: 1},
		{name: "normal", value: 80, want: 80},
		{name: "maximum", value: 65535, want: 65535},
		{name: "overflow", value: 65536, want: 65535},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := terminalDimension(test.value); got != test.want {
				t.Fatalf("terminalDimension(%d) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
