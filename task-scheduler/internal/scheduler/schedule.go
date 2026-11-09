package scheduler

import "time"

// Schedule decides when a task should run next (Strategy pattern).
// Adding a new kind of schedule (e.g. cron) means adding a new type,
// not touching the scheduler.
type Schedule interface {
	// Next returns the next run time. scheduledAt is when the run was due,
	// finishedAt is when it actually finished. ok=false means no more runs.
	Next(scheduledAt, finishedAt time.Time) (next time.Time, ok bool)
}

// OneTime runs exactly once.
type OneTime struct{}

func (OneTime) Next(_, _ time.Time) (time.Time, bool) { return time.Time{}, false }

// FixedRate runs every Interval measured from the *scheduled* start,
// e.g. 10:00, 10:05, 10:10 regardless of how long each run takes.
type FixedRate struct {
	Interval time.Duration
}

func (f FixedRate) Next(scheduledAt, finishedAt time.Time) (time.Time, bool) {
	next := scheduledAt.Add(f.Interval)
	// If a run overran, don't fire a burst of catch-up runs; skip ahead.
	for next.Before(finishedAt) {
		next = next.Add(f.Interval)
	}
	return next, true
}

// FixedDelay waits Delay after the previous run *finishes*.
type FixedDelay struct {
	Delay time.Duration
}

func (f FixedDelay) Next(_, finishedAt time.Time) (time.Time, bool) {
	return finishedAt.Add(f.Delay), true
}
