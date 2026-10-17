package main

import (
	"context"

	"golang.org/x/sys/windows/svc"

	"github.com/YuanZhe-99/Kyokai/internal/helper"
)

// asService runs f under the Windows service manager when this process is
// the KyokaiHelper service, until the manager stops it.
// Returns: handled false when the process is not a service (run f in the
// foreground instead).
func asService(f func(ctx context.Context) error) (handled bool, err error) {
	is, err := svc.IsWindowsService()
	if err != nil || !is {
		return false, err
	}
	return true, svc.Run(helper.ServiceName, service(f))
}

// service adapts a run function to the service manager's control loop.
type service func(ctx context.Context) error

// Execute reports Running, runs the engine and stops it on Stop or
// Shutdown.
func (f service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}
