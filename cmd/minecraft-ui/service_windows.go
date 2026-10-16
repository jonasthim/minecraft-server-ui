//go:build windows

package main

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
)

type nativeService struct {
	ctx context.Context
	run func(context.Context) error
	err error
}

func (h *nativeService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	status <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			h.err = err
			if err != nil {
				return false, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				tick := time.NewTicker(time.Second)
				defer tick.Stop()
				checkpoint := uint32(1)
				for {
					status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: 120000}
					select {
					case err := <-done:
						h.err = err
						if err != nil {
							return false, 1
						}
						return false, 0
					case <-tick.C:
						checkpoint++
					}
				}
			}
		}
	}
}

// runNativeService integrates with SCM when installed as a service. run must own
// supervisor.Shutdown and wait for every JVM before returning on cancellation.
func runNativeService(ctx context.Context, run func(context.Context) error) (bool, error) {
	service, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("detect native service: %w", err)
	}
	if !service {
		return false, nil
	}
	handler := &nativeService{ctx: ctx, run: run}
	if err = svc.Run("MinecraftUI", handler); err != nil {
		return true, err
	}
	return true, handler.err
}
