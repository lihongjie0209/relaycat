package winservice

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("Windows services are only supported on Windows") //nolint:staticcheck // Windows is a proper product name.

type InstallConfig struct {
	Name        string
	DisplayName string
	Description string
	Executable  string
	Arguments   []string
	Automatic   bool
}

type Status struct {
	State     string `json:"state"`
	ProcessID uint32 `json:"process_id,omitempty"`
}

type RunFunc func(context.Context) error
