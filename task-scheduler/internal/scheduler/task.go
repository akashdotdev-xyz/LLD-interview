package scheduler

import (
	"context"
	"time"
)

// Task is the unit of work (Command pattern). The scheduler doesn't care
// what the task does, only that it can be executed.
type Task interface {
	Execute(ctx context.Context) error
}

// TaskFunc lets a plain function be used as a Task.
type TaskFunc func(ctx context.Context) error

func (f TaskFunc) Execute(ctx context.Context) error { return f(ctx) }

type Status int

const (
	Scheduled Status = iota
	Running
	Completed
	Failed
	Cancelled
)

func (s Status) String() string {
	return [...]string{"SCHEDULED", "RUNNING", "COMPLETED", "FAILED", "CANCELLED"}[s]
}

type RetryPolicy struct {
	MaxRetries int
	Backoff    time.Duration
}

// ScheduledTask wraps a Task with everything the scheduler needs to know
// about *when* and *how* to run it.
type ScheduledTask struct {
	ID       string
	task     Task
	schedule Schedule
	retry    RetryPolicy
	priority int // higher runs first when two tasks are due at the same time

	slot     time.Time // when the current run was originally due; retries don't change it
	nextRun  time.Time // when to execute next: slot, or a later time if retrying
	status   Status
	attempts int // failed attempts for the current run
	runs     int // total executions
	index    int // position in the heap, needed for O(log n) cancel
}
