package metrics

import (
	"context"
	"fmt"
	"sync"
	"time"
)

func (s *Service) Sample(ctx context.Context, id string) error {
	now := s.cfg.Now()
	jvmVerified := false
	p := Point{CollectedAt: now.UTC(), Expected: 1, Counts: Counts{Samples: 1}, Sources: []Source{}}
	nativeCtx, cancelNative := context.WithTimeout(ctx, 4*time.Second)
	identity, err := s.cfg.Native.Identity(nativeCtx, id)
	if err == nil {
		var live bool
		observed, e := s.cfg.Native.Inspect(nativeCtx, identity)
		live = e == nil && observed.ProcessRunning
		if live {
			counters, e := s.cfg.Native.SampleMetrics(nativeCtx, identity)
			if e == nil {
				jvmVerified = true
				p.ResidentBytes = counters.ResidentBytes
				p.PrivateBytes = counters.PrivateBytes
				if p.ResidentBytes != nil {
					p.Counts.Resident = 1
				}
				if p.PrivateBytes != nil {
					p.Counts.Private = 1
				}
				reason := "first_sample"
				if counters.CPUNanos != nil {
					s.previousMu.Lock()
					sampleAt := s.cfg.Now()
					old, ok := s.previous[id]
					elapsed := sampleAt.Sub(old.at)
					wallElapsed := sampleAt.Round(0).Sub(old.wall)
					skew := wallElapsed - elapsed
					if skew < 0 {
						skew = -skew
					}
					if ok && old.identity == identity && elapsed > 0 && elapsed <= 90*time.Second && wallElapsed > 0 && skew < time.Second && *counters.CPUNanos >= old.cpu {
						v := float64(*counters.CPUNanos-old.cpu) / float64(elapsed) * 100
						p.CPUPercent = &v
						p.Counts.CPU = 1
						reason = ""
					}
					s.previous[id] = previous{identity: identity, cpu: *counters.CPUNanos, at: sampleAt, wall: sampleAt.Round(0)}
					s.previousMu.Unlock()
				} else {
					s.dropPrevious(id)
					reason = "counters_unavailable"
				}
				p.Sources = append(p.Sources, Source{CPU, SourceKind(counters.Source), counters.ObservedAt, reason}, Source{Resident, SourceKind(counters.Source), counters.ObservedAt, func() string {
					if p.ResidentBytes == nil {
						return "memory_unavailable"
					}
					return ""
				}()}, Source{Private, SourceKind(counters.Source), counters.ObservedAt, func() string {
					if p.PrivateBytes == nil {
						return "not_available_on_platform"
					}
					return ""
				}()})
			} else {
				s.dropPrevious(id)
			}
		} else {
			s.dropPrevious(id)
		}
	} else {
		s.dropPrevious(id)
	}
	cancelNative()
	if len(p.Sources) == 0 {
		p.Sources = append(p.Sources, Source{CPU, "jvm_identity", now.UTC(), "jvm_unavailable"}, Source{Resident, "jvm_identity", now.UTC(), "jvm_unavailable"}, Source{Private, "jvm_identity", now.UTC(), "jvm_unavailable"})
	}
	diskCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	disk, e := s.disk(diskCtx, id)
	cancel()
	reason := ""
	if e == nil {
		p.DiskBytes = &disk
		p.Counts.Disk = 1
	} else {
		reason = "bounded_disk_scan_unavailable"
	}
	p.Sources = append(p.Sources, Source{Disk, "instance_logical_bytes", now.UTC(), reason})
	reason = "native_roster_unavailable"
	playerAt := now.UTC()
	if s.cfg.Players != nil {
		playerCtx, cancelPlayers := context.WithTimeout(ctx, time.Second)
		snapshot, e := s.cfg.Players.Snapshot(playerCtx, id)
		cancelPlayers()
		playerAt = snapshot.ObservedAt
		if e == nil && jvmVerified && snapshot.Known && snapshot.Complete && !snapshot.Gap && snapshot.Count >= 0 && snapshot.Generation == fmt.Sprintf("%d:%s", identity.PID, identity.StartIdentity) && now.Sub(snapshot.ObservedAt) >= 0 && now.Sub(snapshot.ObservedAt) <= 45*time.Second {
			v := float64(snapshot.Count)
			p.Players = &v
			p.Counts.Players = 1
			reason = ""
		}
	}
	p.Sources = append(p.Sources, Source{Players, "task11_native_roster", playerAt, reason})
	p.Gap = p.CPUPercent == nil || p.ResidentBytes == nil || p.DiskBytes == nil || p.Players == nil
	return s.store(ctx, id, p)
}

func (s *Service) dropPrevious(id string) {
	s.previousMu.Lock()
	delete(s.previous, id)
	s.previousMu.Unlock()
}

// Run owns two bounded workers and drains them before returning. Collection
// overload leaves explicit missing buckets; round-robin admission prevents a
// slow/large instance from permanently starving later instances.
func (s *Service) Run(ctx context.Context) error {
	if s.cfg.DB == nil || s.cfg.Instances == nil || s.cfg.Native == nil {
		return fmt.Errorf("metrics dependencies unavailable")
	}
	workerCtx, cancel := context.WithCancel(ctx)
	work := make(chan string, 2)
	fatal := make(chan error, 1)
	var workers sync.WaitGroup
	var pendingMu sync.Mutex
	pending := map[string]bool{}
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range work {
				err := s.Sample(workerCtx, id)
				pendingMu.Lock()
				delete(pending, id)
				pendingMu.Unlock()
				if err != nil && workerCtx.Err() == nil {
					select {
					case fatal <- fmt.Errorf("record JVM metrics: %w", err):
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	defer func() { cancel(); close(work); workers.Wait() }()
	tick := time.NewTicker(Interval)
	defer tick.Stop()
	offset := 0
	lastRetention := time.Time{}
	for {
		instances, err := s.cfg.Instances.List(workerCtx)
		if err != nil {
			select {
			case failure := <-fatal:
				return failure
			default:
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("list metric instances: %w", err)
		}
		validIDs := map[string]bool{}
		for _, detail := range instances {
			validIDs[detail.Instance.ID] = true
		}
		s.previousMu.Lock()
		for id := range s.previous {
			if !validIDs[id] {
				delete(s.previous, id)
			}
		}
		s.previousMu.Unlock()
		if len(instances) > 0 {
			for n := 0; n < len(instances); n++ {
				id := instances[(offset+n)%len(instances)].Instance.ID
				pendingMu.Lock()
				if !pending[id] {
					select {
					case work <- id:
						pending[id] = true
					default:
					}
				}
				pendingMu.Unlock()
			}
			offset = (offset + 4) % len(instances)
		}
		if s.cfg.Now().Sub(lastRetention) >= time.Hour {
			if err = s.Retain(workerCtx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("retain metrics: %w", err)
			}
			lastRetention = s.cfg.Now()
		}
		select {
		case failure := <-fatal:
			return failure
		case <-ctx.Done():
			return nil
		case <-workerCtx.Done():
			select {
			case failure := <-fatal:
				return failure
			default:
				return nil
			}
		case <-tick.C:
		}
	}
}
