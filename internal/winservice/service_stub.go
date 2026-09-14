//go:build !windows && !linux

package winservice

import "context"

func Install(InstallConfig) error        { return ErrUnsupported }
func Uninstall(string) error             { return ErrUnsupported }
func Start(string) error                 { return ErrUnsupported }
func Stop(context.Context, string) error { return ErrUnsupported }
func Query(string) (Status, error)       { return Status{}, ErrUnsupported }
func Run(string, RunFunc) error          { return ErrUnsupported }
