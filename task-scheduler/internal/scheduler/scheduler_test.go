package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func counter(n *atomic.Int32) TaskFunc {
	return func(ctx context.Context) error { n.Add(1); return nil }
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOneTimeRunsOnce(t *testing.T) {
	s := NewScheduler(2)
	s.Start()
	defer s.Stop()

	var n atomic.Int32
	id, _ := s.ScheduleOnce(counter(&n), 20*time.Millisecond)

	waitFor(t, func() bool { st, _ := s.Status(id); return st == Completed })
	time.Sleep(50 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("want 1 run, got %d", n.Load())
	}
}

func TestEarlierTaskRunsFirst(t *testing.T) {
	s := NewScheduler(1)
	s.Start()
	defer s.Stop()

	var mu sync.Mutex
	var order []string
	rec := func(name string) TaskFunc {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	// Scheduled later but due sooner: dispatcher must wake up for it.
	s.ScheduleOnce(rec("late"), 80*time.Millisecond)
	s.ScheduleOnce(rec("early"), 20*time.Millisecond)

	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 2 })
	if order[0] != "early" || order[1] != "late" {
		t.Fatalf("wrong order: %v", order)
	}
}

func TestFixedRateRepeats(t *testing.T) {
	s := NewScheduler(2)
	s.Start()
	defer s.Stop()

	var n atomic.Int32
	s.ScheduleAtFixedRate(counter(&n), 0, 20*time.Millisecond)
	waitFor(t, func() bool { return n.Load() >= 3 })
}

func TestCancelStopsFutureRuns(t *testing.T) {
	s := NewScheduler(2)
	s.Start()
	defer s.Stop()

	var n atomic.Int32
	id, _ := s.ScheduleWithFixedDelay(counter(&n), 0, 20*time.Millisecond)
	waitFor(t, func() bool { return n.Load() >= 2 })

	if !s.Cancel(id) {
		t.Fatal("cancel returned false")
	}
	time.Sleep(10 * time.Millisecond) // let an in-flight run finish
	got := n.Load()
	time.Sleep(80 * time.Millisecond)
	if n.Load() != got {
		t.Fatalf("task ran after cancel: %d -> %d", got, n.Load())
	}
	if st, _ := s.Status(id); st != Cancelled {
		t.Fatalf("want CANCELLED, got %v", st)
	}
}

func TestRetryThenFail(t *testing.T) {
	s := NewScheduler(1)
	s.Start()
	defer s.Stop()

	var n atomic.Int32
	failing := TaskFunc(func(ctx context.Context) error { n.Add(1); return errors.New("boom") })
	id, _ := s.ScheduleOnce(failing, 0, WithRetry(2, 10*time.Millisecond))

	waitFor(t, func() bool { st, _ := s.Status(id); return st == Failed })
	if n.Load() != 3 { // 1 attempt + 2 retries
		t.Fatalf("want 3 attempts, got %d", n.Load())
	}
}

func TestPanickingTaskDoesNotKillWorker(t *testing.T) {
	s := NewScheduler(1)
	s.Start()
	defer s.Stop()

	s.ScheduleOnce(TaskFunc(func(ctx context.Context) error { panic("oops") }), 0)
	var n atomic.Int32
	s.ScheduleOnce(counter(&n), 20*time.Millisecond)
	waitFor(t, func() bool { return n.Load() == 1 })
}

// fillWorker occupies the only worker until the returned func is called or
// the scheduler stops.
func fillWorker(t *testing.T, s *Scheduler) (release func()) {
	t.Helper()
	started, done := make(chan struct{}), make(chan struct{})
	s.ScheduleOnce(TaskFunc(func(ctx context.Context) error {
		close(started)
		select {
		case <-done:
		case <-ctx.Done():
		}
		return nil
	}), 0)
	<-started
	return func() { close(done) }
}

func TestCancelWhileWaitingForWorker(t *testing.T) {
	s := NewScheduler(1)
	s.Start()
	defer s.Stop()
	release := fillWorker(t, s)

	var n atomic.Int32
	id, _ := s.ScheduleOnce(counter(&n), 0)
	time.Sleep(20 * time.Millisecond) // dispatcher pops it and blocks in Submit

	if !s.Cancel(id) {
		t.Fatal("cancel returned false")
	}
	release()
	time.Sleep(30 * time.Millisecond)

	if n.Load() != 0 {
		t.Fatal("cancelled task still ran")
	}
	if st, _ := s.Status(id); st != Cancelled {
		t.Fatalf("want CANCELLED, got %v", st)
	}
}

func TestStopCancelsPendingTasks(t *testing.T) {
	s := NewScheduler(1)
	s.Start()
	fillWorker(t, s)

	var n atomic.Int32
	popped, _ := s.ScheduleOnce(counter(&n), 0) // popped, waiting for a worker
	time.Sleep(20 * time.Millisecond)
	later, _ := s.ScheduleOnce(counter(&n), time.Hour) // still in the heap

	s.Stop()

	for _, id := range []string{popped, later} {
		if st, _ := s.Status(id); st != Cancelled {
			t.Fatalf("%s: want CANCELLED, got %v", id, st)
		}
	}
	if n.Load() != 0 {
		t.Fatal("task ran after stop")
	}
}

func TestScheduleAfterStopReturnsError(t *testing.T) {
	s := NewScheduler(1)
	s.Start()
	s.Stop()

	var n atomic.Int32
	if _, err := s.ScheduleOnce(counter(&n), 0); !errors.Is(err, ErrStopped) {
		t.Fatalf("want ErrStopped, got %v", err)
	}
}

func TestInvalidIntervalRejected(t *testing.T) {
	s := NewScheduler(1)
	var n atomic.Int32
	if _, err := s.ScheduleAtFixedRate(counter(&n), 0, 0); !errors.Is(err, ErrInvalidInterval) {
		t.Fatalf("fixed rate: want ErrInvalidInterval, got %v", err)
	}
	if _, err := s.ScheduleWithFixedDelay(counter(&n), 0, -time.Second); !errors.Is(err, ErrInvalidInterval) {
		t.Fatalf("fixed delay: want ErrInvalidInterval, got %v", err)
	}
}

// Calls run directly on a scheduler that isn't started, so no timing is involved.
func TestRetryDoesNotShiftFixedRateSchedule(t *testing.T) {
	s := NewScheduler(1)
	var calls int
	flaky := TaskFunc(func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("boom")
		}
		return nil
	})
	s.ScheduleAtFixedRate(flaky, 0, time.Hour, WithRetry(1, time.Minute))
	first := heap.Pop(&s.queue).(*ScheduledTask)
	slot := first.slot

	s.run(first) // fails: retried in about a minute
	heap.Pop(&s.queue)
	if !first.nextRun.After(slot) || !first.slot.Equal(slot) {
		t.Fatalf("retry should move nextRun only: slot %v, nextRun %v", first.slot, first.nextRun)
	}

	s.run(first) // succeeds: next run is measured from the original slot
	if want := slot.Add(time.Hour); !first.nextRun.Equal(want) {
		t.Fatalf("want next run %v, got %v", want, first.nextRun)
	}
}

func TestOldestFinishedTaskIsForgotten(t *testing.T) {
	s := NewScheduler(1)
	s.maxFinished = 2

	var ids []string
	for i := 0; i < 3; i++ {
		id, _ := s.ScheduleOnce(TaskFunc(func(ctx context.Context) error { return nil }), time.Hour)
		s.Cancel(id)
		ids = append(ids, id)
	}

	if _, ok := s.Status(ids[0]); ok {
		t.Fatal("oldest finished task should be forgotten")
	}
	if st, ok := s.Status(ids[2]); !ok || st != Cancelled {
		t.Fatalf("newest finished task: got %v, %v", st, ok)
	}
}
