//go:build windows

package cmd

import (
	"context"
	"fmt"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/setup"
	"golang.org/x/sys/windows/svc"
)

// MaybeRunAsWindowsService detects Windows service context and runs service dispatcher when needed.
// Returns handled=true when running as a Windows service.
func MaybeRunAsWindowsService() (handled bool, err error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		writeStartupTrace("MaybeRunAsWindowsService detection failed: %v", err)
		return false, fmt.Errorf("failed to detect windows service context: %w", err)
	}
	if !isService {
		return false, nil
	}

	logger.Info("Detected Windows service context, starting service dispatcher...")
	if err := svc.Run(setup.GetServiceName(), &aliangWindowsService{}); err != nil {
		writeStartupTrace("svc.Run failed: %v", err)
		return true, fmt.Errorf("windows service runtime failed: %w", err)
	}
	return true, nil
}

type aliangWindowsService struct{}

func (s *aliangWindowsService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	if err := applyWindowsServiceRuntimeArgs(args); err != nil {
		logger.Error("Invalid Windows service runtime arguments", "error", err)
		return false, 1
	}

	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runCoreWithContext(ctx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, Accepts: accepted}
				cancel()
				if err := <-done; err != nil {
					logger.Error("Windows service core loop exited with error", "error", err)
					return false, 1
				}
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
			}
		case err := <-done:
			writeStartupTrace("windows service core goroutine exited err=%v", err)
			if err != nil {
				logger.Error("Windows service core loop exited unexpectedly", "error", err)
				return false, 1
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func applyWindowsServiceRuntimeArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		case "--host":
			if i+1 >= len(args) {
				return fmt.Errorf("--host requires a value")
			}
			if err := config.SetServiceBindHost(args[i+1]); err != nil {
				return err
			}
			i++
		}
	}
	return nil
}
