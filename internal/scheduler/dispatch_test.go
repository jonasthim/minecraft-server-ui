package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestStartupReconcileDrainsAllDueSchedulesAtFixedCutoff(t *testing.T) {
	database, s, now, producer, user := setup(t)
	if _, err := database.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('beta','Beta','vanilla','1.21.9',21,1024,25566)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		instance := "alpha"
		if i == 100 {
			instance = "beta"
		}
		if _, err := s.Save(t.Context(), instance, "", Input{Name: fmt.Sprintf("List %d", i), Enabled: true, Action: domain.ScheduleCommand, Command: "list", Expression: "* * * * *"}, user); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := at("2026-10-16T12:01:10Z")
	clockCalls := 0
	s.now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return cutoff
		}
		return cutoff.Add(2 * time.Minute)
	}
	if err := s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	var skipped, old, next int
	if err := database.QueryRow(`SELECT count(*) FROM schedule_runs WHERE state='skipped_downtime'`).Scan(&skipped); err != nil {
		t.Fatal(err)
	}
	if skipped != 101 {
		t.Fatalf("startup skipped %d schedules; want all 101 before normal dispatch", skipped)
	}
	if err := database.QueryRow(`SELECT count(*) FROM schedules WHERE enabled=1 AND next_run_at<=?`, stamp(cutoff)).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM schedules WHERE next_run_at=?`, stamp(at("2026-10-16T12:02:00Z"))).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if old != 0 || next != 101 || clockCalls != 1 {
		t.Fatal("startup cutoff drifted or left downtime work", old, next, clockCalls)
	}
	// The first normal tick remains under one minute late. It must not execute
	// the occurrence that fell outside the first startup batch.
	*now = cutoff
	s.now = func() time.Time { return *now }
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if producer.calls != 0 {
		t.Fatal("startup downtime work executed on first tick", producer.calls)
	}
}

func TestStartupReconcileCancellationReturnsBeforeAdmission(t *testing.T) {
	_, s, now, producer, user := setup(t)
	if _, err := s.Save(t.Context(), "alpha", "", Input{Name: "List", Enabled: true, Action: domain.ScheduleCommand, Command: "list", Expression: "* * * * *"}, user); err != nil {
		t.Fatal(err)
	}
	*now = at("2026-10-16T12:01:10Z")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Reconcile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if producer.calls != 0 {
		t.Fatal("cancelled startup admitted work", producer.calls)
	}
	// Retrying startup completes the skipped history before the normal timer.
	if err := s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if producer.calls != 0 {
		t.Fatal("retry admitted downtime work", producer.calls)
	}
}
