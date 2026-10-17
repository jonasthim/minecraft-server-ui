//go:build windows

package main

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"golang.org/x/sys/windows/svc"
)

type negativeService struct {
	ctx   context.Context
	probe bool
}

func (s negativeService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	if s.probe {
		// Real shipping challenge/image/schema probe writer; no invented successful response.
		if selfupdate.WriteSchemaProbeResult(s.ctx, `C:\ProgramData\MinecraftUI`, version, db.LatestVersion()) != nil {
			return true, 1
		}
		return false, 0
	}
	for {
		select {
		case r := <-requests:
			if r.Cmd == svc.Stop || r.Cmd == svc.Shutdown {
				status <- svc.Status{State: svc.StopPending}
				return false, 0
			}
		case <-s.ctx.Done():
			return false, 0
		}
	}
}
func probeService(ctx context.Context) error {
	return svc.Run("MinecraftUIUpdateProbe", negativeService{ctx, true})
}
func unhealthyService(ctx context.Context) error {
	return svc.Run("MinecraftUI", negativeService{ctx, false})
}
