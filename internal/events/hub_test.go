package events

import "testing"

// Dropping disconnects a slow reader so publishers never wait or hide a gap.
func TestSlowSubscriberDisconnectsAndCloseIsSafe(t *testing.T) {
	h := NewHub(1)
	sub := h.Subscribe("job-a")
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
	h.Close()
	h.Publish(Event{JobID: "job-a"})
	h.Close()
	if _, ok := <-h.Subscribe("").Events; ok {
		t.Fatal("subscribe after close must close immediately")
	}
}
