//go:build windows

package winservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func Install(cfg InstallConfig) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to Windows Service Control Manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()
	if existing, openErr := m.OpenService(cfg.Name); openErr == nil {
		_ = existing.Close()
		return fmt.Errorf("Windows service %q already exists", cfg.Name)
	}
	startType := uint32(mgr.StartManual)
	if cfg.Automatic {
		startType = mgr.StartAutomatic
	}
	serviceArgs := []string{"service", "run", "--name", cfg.Name, "--"}
	serviceArgs = append(serviceArgs, cfg.Arguments...)
	service, err := m.CreateService(cfg.Name, cfg.Executable, mgr.Config{
		DisplayName: cfg.DisplayName,
		Description: cfg.Description,
		StartType:   startType,
	}, serviceArgs...)
	if err != nil {
		return fmt.Errorf("creating Windows service %q: %w", cfg.Name, err)
	}
	return service.Close()
}

func Uninstall(name string) error {
	return withService(name, func(service *mgr.Service) error {
		if err := service.Delete(); err != nil {
			return fmt.Errorf("deleting Windows service %q: %w", name, err)
		}
		return nil
	})
}

func Start(name string) error {
	return withService(name, func(service *mgr.Service) error {
		if err := service.Start(); err != nil {
			return fmt.Errorf("starting Windows service %q: %w", name, err)
		}
		return nil
	})
}

func Stop(ctx context.Context, name string) error {
	return withService(name, func(service *mgr.Service) error {
		status, err := service.Control(svc.Stop)
		if err != nil {
			return fmt.Errorf("stopping Windows service %q: %w", name, err)
		}
		for status.State != svc.Stopped {
			select {
			case <-ctx.Done():
				return fmt.Errorf("waiting for Windows service %q to stop: %w", name, ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
			status, err = service.Query()
			if err != nil {
				return fmt.Errorf("querying Windows service %q: %w", name, err)
			}
		}
		return nil
	})
}

func Query(name string) (Status, error) {
	var result Status
	err := withService(name, func(service *mgr.Service) error {
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("querying Windows service %q: %w", name, err)
		}
		result = Status{State: stateName(status.State), ProcessID: status.ProcessId}
		return nil
	})
	return result, err
}

func Run(name string, run RunFunc) error {
	if err := svc.Run(name, &handler{run: run}); err != nil {
		return fmt.Errorf("running Windows service %q: %w", name, err)
	}
	return nil
}

type handler struct {
	run RunFunc
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	statuses <- svc.Status{State: svc.StartPending}
	go func() { done <- h.run(ctx) }()
	statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			statuses <- svc.Status{State: svc.Stopped}
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			return false, 0
		case request, ok := <-requests:
			if !ok {
				cancel()
				<-done
				return false, 0
			}
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending}
				cancel()
			}
		}
	}
}

func withService(name string, fn func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to Windows Service Control Manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()
	service, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("opening Windows service %q: %w", name, err)
	}
	defer func() { _ = service.Close() }()
	return fn(service)
}

func stateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown-%d", state)
	}
}
