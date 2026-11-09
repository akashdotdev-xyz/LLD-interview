package scheduler

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolRunsJobsWithinSizeLimit(t *testing.T) {
	p := NewWorkerPool(2)
	p.Start()

	var done, running, peak atomic.Int32
	for i := 0; i < 6; i++ {
		ok := p.Submit(func() {
			cur := running.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
			done.Add(1)
		})
		if !ok {
			t.Fatal("submit rejected before stop")
		}
	}
	p.Stop()

	if done.Load() != 6 {
		t.Fatalf("want 6 jobs done, got %d", done.Load())
	}
	if peak.Load() > 2 {
		t.Fatalf("want at most 2 concurrent jobs, got %d", peak.Load())
	}
}

func TestPoolSubmitAfterStopReturnsFalse(t *testing.T) {
	p := NewWorkerPool(1)
	p.Start()
	p.Stop()

	if p.Submit(func() { t.Error("job ran after stop") }) {
		t.Fatal("want submit to return false after stop")
	}
}
