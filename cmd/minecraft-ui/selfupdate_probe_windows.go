//go:build windows

package main

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"golang.org/x/sys/windows/svc"
	"time"
)

type schemaProbeService struct{ ctx context.Context }

func (s schemaProbeService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	done := make(chan error, 1)
	go func() { done <- selfupdate.WriteSchemaProbeResult(ctx, `C:\ProgramData\MinecraftUI`, version) }()
	for {
		select {
		case err := <-done:
			if err != nil {
				return true, 1
			}
			return false, 0
		case request := <-requests:
			if request.Cmd == svc.Stop || request.Cmd == svc.Shutdown {
				cancel()
				status <- svc.Status{State: svc.StopPending}
				if err := <-done; err != nil {
					return true, 1
				}
				return false, 0
			}
		case <-ctx.Done():
			return true, 1
		}
	}
}
func runSelfUpdateProbeService(ctx context.Context) error {
	return svc.Run("MinecraftUIUpdateProbe", schemaProbeService{ctx})
}
