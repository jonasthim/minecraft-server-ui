package events

import (
	"sync"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// Dropping disconnects a slow reader so publishers never wait or hide a gap.
func TestSlowSubscriberDisconnectsAndCloseIsSafe(t *testing.T) {
	h := NewHubWithLimit(1, 1)
	sub, err := h.Subscribe("job-a")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	h.Publish(Event{JobID: "other", Type: "progress"})
	h.Publish(Event{JobID: "job-a", Type: "progress"})
	h.Publish(Event{JobID: "job-a", Type: "progress"})
	if event, ok := <-sub.Events; !ok || event.JobID != "job-a" {
		t.Fatal("subscriber lost its authorized job filter or first buffered event")
	}
	if _, ok := <-sub.Events; ok {
		t.Fatal("slow subscriber must disconnect")
	}
	replacement, err := h.Subscribe("job-b")
	if err != nil {
		t.Fatalf("slow subscriber leaked its slot: %v", err)
	}
	replacement.Close()
	h.Close()
	h.Publish(Event{JobID: "job-a"})
	h.Close()
	if sub, err := h.Subscribe(""); sub != nil || domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("closed hub admitted subscription: %v %v", sub, err)
	}
}

func TestSubscriberQuotaIsGlobalAndCloseReleasesAdmission(t *testing.T) {
	h := NewHubWithLimit(2, 2)
	first, err := h.Subscribe("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Subscribe("different-job")
	if err != nil {
		t.Fatal(err)
	}
	if sub, err := h.Subscribe(""); sub != nil || domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("saturated hub admitted subscription: %v %v", sub, err)
	}
	first.Close()
	first.Close()
	replacement, err := h.Subscribe("third")
	if err != nil {
		t.Fatalf("Close leaked quota: %v", err)
	}
	replacement.Close()
	second.Close()
	h.Close()
}

func TestConcurrentSubscriberAdmissionStaysBounded(t *testing.T) {
	h := NewHubWithLimit(1, 4)
	defer h.Close()
	var wg sync.WaitGroup
	admitted := make(chan *Subscription, 100)
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			if sub, err := h.Subscribe(""); err == nil {
				admitted <- sub
			} else if domain.AsError(err).Code != domain.CodeUnavailable {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()
	close(admitted)
	if len(admitted) != 4 {
		t.Fatalf("admitted %d subscribers, want 4", len(admitted))
	}
	for sub := range admitted {
		sub.Close()
	}
	if sub, err := h.Subscribe(""); err != nil {
		t.Fatalf("concurrent cleanup leaked quota: %v", err)
	} else {
		sub.Close()
	}
}
