package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"task-scheduler/internal/scheduler"
)

func main() {
	start := time.Now()
	log := func(format string, args ...any) {
		fmt.Printf("[%5dms] %s\n", time.Since(start).Milliseconds(), fmt.Sprintf(format, args...))
	}

	s := scheduler.NewScheduler(3)
	s.Start()

	s.ScheduleOnce(scheduler.TaskFunc(func(ctx context.Context) error {
		log("send welcome email (one-time)")
		return nil
	}), 300*time.Millisecond)

	heartbeat, _ := s.ScheduleAtFixedRate(scheduler.TaskFunc(func(ctx context.Context) error {
		log("heartbeat (fixed rate 200ms)")
		return nil
	}), 0, 200*time.Millisecond)

	s.ScheduleWithFixedDelay(scheduler.TaskFunc(func(ctx context.Context) error {
		log("cleanup start (fixed delay 250ms, takes 100ms)")
		time.Sleep(100 * time.Millisecond)
		return nil
	}), 100*time.Millisecond, 250*time.Millisecond)

	attempt := 0
	flaky, _ := s.ScheduleOnce(scheduler.TaskFunc(func(ctx context.Context) error {
		attempt++
		log("flaky payment call, attempt %d", attempt)
		if attempt < 3 {
			return errors.New("timeout")
		}
		return nil
	}), 150*time.Millisecond, scheduler.WithRetry(3, 100*time.Millisecond))

	time.Sleep(700 * time.Millisecond)
	log("cancelling heartbeat: %v", s.Cancel(heartbeat))

	time.Sleep(500 * time.Millisecond)
	st, _ := s.Status(flaky)
	log("flaky task status: %v", st)

	s.Stop()
	log("scheduler stopped")
}
