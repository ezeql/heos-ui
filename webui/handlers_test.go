package webui

import (
	"sync"
	"testing"

	"github.com/ezeql/heoscli-go/position"
)

// Every open tab polls /api/state on its own goroutine while seek
// re-anchors the same clock.
func TestClockAcrossConcurrentRequests(t *testing.T) {
	t.Parallel()
	s := &Server{}
	if _, ok := s.clockReading(true); ok {
		t.Error("read a clock before anything was played")
	}
	s.clock = position.NewClock(0)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			// Alternating play/pause makes every Sync write.
			if _, ok := s.clockReading(i%2 == 0); !ok {
				t.Error("clock missing")
			}
			s.anchorClock(float64(i))
		})
	}
	wg.Wait()

	s.anchorClock(60)
	if ms, _ := s.clockReading(false); ms < 60_000 || ms > 61_000 {
		t.Errorf("position after seeking to 60s = %dms", ms)
	}
}
