package metrics

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type fleetInstances struct{ details []instance.Detail }

func (f fleetInstances) List(context.Context) ([]instance.Detail, error) { return f.details, nil }
func (f fleetInstances) Get(context.Context, string) (instance.Detail, error) {
	return instance.Detail{}, nil
}

type fleetNative struct {
	fakeNative
	delay     time.Duration
	mu        sync.Mutex
	active    map[string]bool
	maximum   int
	duplicate bool
}

func (f *fleetNative) Identity(ctx context.Context, id string) (domain.ProcessIdentity, error) {
	f.mu.Lock()
	if f.active[id] {
		f.duplicate = true
	}
	f.active[id] = true
	if len(f.active) > f.maximum {
		f.maximum = len(f.active)
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); delete(f.active, id); f.mu.Unlock() }()
	select {
	case <-time.After(f.delay):
		return domain.ProcessIdentity{InstanceID: id}, nil
	case <-ctx.Done():
		return domain.ProcessIdentity{}, ctx.Err()
	}
}
func admissionFixture(t *testing.T, count int, delay time.Duration) (*Service, *fleetNative) {
	s, _, _, _ := metricFixture(t)
	fleet := fleetInstances{}
	for n := 0; n < count; n++ {
		id := fmt.Sprintf("fleet-%02d", n)
		if _, e := s.cfg.DB.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port)VALUES(?,?,'vanilla','1.21.9',21,1024,?)`, id, id, 26000+n); e != nil {
			t.Fatal(e)
		}
		fleet.details = append(fleet.details, instance.Detail{Instance: domain.Instance{ID: id}})
	}
	native := &fleetNative{fakeNative: fakeNative{fail: true}, delay: delay, active: map[string]bool{}}
	s.cfg.Native = native
	s.cfg.Instances = fleet
	s.cfg.Players = nil
	s.cfg.Now = time.Now
	return s, native
}
func TestSamplingAdmitsHealthyFleetWithinPeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, native := admissionFixture(t, 7, 10*time.Millisecond)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		var sampled int
		if e := s.cfg.DB.QueryRow(`SELECT COUNT(DISTINCT instance_id) FROM metric_samples`).Scan(&sampled); e != nil {
			t.Fatal(e)
		}
		cancel()
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		if sampled != 7 {
			t.Fatalf("healthy fleet lost samples despite spare period capacity: got %d/7", sampled)
		}
		if native.maximum > 2 || native.duplicate {
			t.Fatal("collector exceeded bounded unique admission", native.maximum, native.duplicate)
		}
	})
}
func TestSamplingOverloadLeavesGapsAndCancellationDrains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, native := admissionFixture(t, 40, time.Hour)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
		time.Sleep(31 * time.Second)
		synctest.Wait()
		var sampled int
		if e := s.cfg.DB.QueryRow(`SELECT COUNT(DISTINCT instance_id) FROM metric_samples`).Scan(&sampled); e != nil {
			t.Fatal(e)
		}
		time.Sleep(60 * time.Second)
		synctest.Wait()
		var total int
		if e := s.cfg.DB.QueryRow(`SELECT COUNT(DISTINCT instance_id) FROM metric_samples`).Scan(&total); e != nil {
			t.Fatal(e)
		}
		if total != 40 {
			t.Fatalf("overload starved later instances: sampled %d/40", total)
		}
		cancel()
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		if sampled == 0 || sampled >= 40 {
			t.Fatalf("overload did not preserve honest gaps: sampled %d/40", sampled)
		}
		native.mu.Lock()
		defer native.mu.Unlock()
		if len(native.active) != 0 || native.maximum > 2 || native.duplicate {
			t.Fatal("overload leaked or duplicated workers", native.active, native.maximum, native.duplicate)
		}
	})
}
