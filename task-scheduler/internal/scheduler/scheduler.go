package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrStopped         = errors.New("scheduler is stopped")
	ErrInvalidInterval = errors.New("interval must be positive")
)

// defaultMaxFinished is how many finished tasks stay queryable through Status.
const defaultMaxFinished = 1000

// Scheduler decides what runs and when. The WorkerPool does the running.
//
//	Schedule() ──push──▶ [ min-heap by nextRun ] ◀──reschedule── run()
//	                            │
//	               dispatcher sleeps until heap top is due
//	                            │
//	                            ▼
//	                 pool.Submit(run) ──▶ worker 1..N ──▶ task.Execute()
type Scheduler struct {
	mu          sync.Mutex
	queue       TaskQueue
	tasks       map[string]*ScheduledTask
	finished    []string // IDs in the order they finished, oldest first
	maxFinished int

	pool     *WorkerPool
	wake     chan struct{}   // nudges the dispatcher when the heap top may have changed
	ctx      context.Context // cancelled by Stop; tells the dispatcher and running tasks to stop
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once

	nextID  atomic.Int64
	started atomic.Bool
}

func NewScheduler(workers int) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		tasks:       make(map[string]*ScheduledTask),
		maxFinished: defaultMaxFinished,
		pool:        NewWorkerPool(workers),
		wake:        make(chan struct{}, 1),
		ctx:         ctx,
		cancel:      cancel,
	}
}

func (s *Scheduler) Start() {
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	s.pool.Start()
	s.wg.Add(1)
	go s.dispatch()
}

// Stop cancels the context passed to running tasks and waits for them to
// finish. Tasks that haven't started are marked CANCELLED.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		s.cancel()
		s.pool.Stop() // also unblocks a dispatcher waiting in Submit
		s.wg.Wait()

		s.mu.Lock()
		defer s.mu.Unlock()
		// Nothing is running now, so every unfinished task is still SCHEDULED.
		for _, t := range s.tasks {
			if t.status == Scheduled {
				s.finish(t, Cancelled)
			}
		}
		s.queue = nil
	})
}

// ---------- public API (mirrors Java's ScheduledExecutorService) ----------

func (s *Scheduler) ScheduleOnce(task Task, delay time.Duration, opts ...Option) (string, error) {
	return s.submit(task, delay, OneTime{}, opts)
}

func (s *Scheduler) ScheduleAtFixedRate(task Task, initialDelay, interval time.Duration, opts ...Option) (string, error) {
	if interval <= 0 {
		return "", ErrInvalidInterval
	}
	return s.submit(task, initialDelay, FixedRate{Interval: interval}, opts)
}

func (s *Scheduler) ScheduleWithFixedDelay(task Task, initialDelay, delay time.Duration, opts ...Option) (string, error) {
	if delay <= 0 {
		return "", ErrInvalidInterval
	}
	return s.submit(task, initialDelay, FixedDelay{Delay: delay}, opts)
}

// Cancel stops future runs. A run already in progress is allowed to finish
// but won't be rescheduled.
func (s *Scheduler) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok || t.status == Completed || t.status == Failed || t.status == Cancelled {
		return false
	}
	if t.index >= 0 {
		heap.Remove(&s.queue, t.index)
	}
	s.finish(t, Cancelled)
	s.notify()
	return true
}

// Status returns false for unknown IDs, and for finished tasks once they've
// aged out of the last maxFinished.
func (s *Scheduler) Status(id string) (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return 0, false
	}
	return t.status, true
}

// ---------- options ----------

type Option func(*ScheduledTask)

func WithPriority(p int) Option { return func(t *ScheduledTask) { t.priority = p } }

func WithRetry(maxRetries int, backoff time.Duration) Option {
	return func(t *ScheduledTask) { t.retry = RetryPolicy{MaxRetries: maxRetries, Backoff: backoff} }
}

// ---------- internals ----------

func (s *Scheduler) submit(task Task, delay time.Duration, sched Schedule, opts []Option) (string, error) {
	firstRun := time.Now().Add(delay)
	t := &ScheduledTask{
		ID:       fmt.Sprintf("task-%d", s.nextID.Add(1)),
		task:     task,
		schedule: sched,
		slot:     firstRun,
		nextRun:  firstRun,
		status:   Scheduled,
		index:    -1,
	}
	for _, opt := range opts {
		opt(t)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Checked under the lock: if this passes, Stop's cleanup hasn't run yet
	// and will see this task.
	if s.ctx.Err() != nil {
		return "", ErrStopped
	}
	s.tasks[t.ID] = t
	heap.Push(&s.queue, t)
	s.notify()
	return t.ID, nil
}

// notify does a non-blocking send so callers never block on the dispatcher.
// The buffer of 1 means multiple nudges collapse into one, which is fine:
// the dispatcher re-reads the heap anyway.
func (s *Scheduler) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) dispatch() {
	defer s.wg.Done()

	for {
		due, wait := s.popDue()

		// Submit outside the lock: if all workers are busy this blocks, and
		// workers need the lock to reschedule, so holding it would deadlock.
		for _, t := range due {
			if !s.pool.Submit(func() { s.run(t) }) {
				return
			}
		}
		if len(due) > 0 {
			continue // time has passed while handing off; re-check the heap
		}

		// Heap empty: a nil channel blocks forever, so we only wait for wake.
		var timer *time.Timer
		var fire <-chan time.Time
		if wait >= 0 {
			timer = time.NewTimer(wait)
			fire = timer.C
		}

		select {
		case <-fire:
		case <-s.wake:
		case <-s.ctx.Done():
		}
		if timer != nil {
			timer.Stop()
		}
		if s.ctx.Err() != nil {
			return
		}
	}
}

// popDue removes every task whose time has come. They stay SCHEDULED until a
// worker picks them up, so Cancel can still stop them. If none are due it
// returns how long until the earliest one (-1 if the heap is empty).
func (s *Scheduler) popDue() ([]*ScheduledTask, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var due []*ScheduledTask
	for s.queue.Len() > 0 {
		top := s.queue.Peek()
		if top.nextRun.After(now) {
			if len(due) > 0 {
				return due, 0
			}
			return nil, top.nextRun.Sub(now)
		}
		heap.Pop(&s.queue)
		due = append(due, top)
	}
	return due, -1
}

func (s *Scheduler) run(t *ScheduledTask) {
	s.mu.Lock()
	// Cancelled while waiting for a worker, or the scheduler is stopping.
	if t.status == Cancelled || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	t.status = Running
	slot := t.slot
	s.mu.Unlock()

	err := safeExecute(s.ctx, t.task)
	finishedAt := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	t.runs++
	if t.status == Cancelled { // cancelled while running
		return
	}

	if err != nil && t.attempts < t.retry.MaxRetries {
		t.attempts++
		s.requeue(t, finishedAt.Add(t.retry.Backoff)) // slot stays, so retries don't shift the schedule
		return
	}
	t.attempts = 0

	if next, ok := t.schedule.Next(slot, finishedAt); ok {
		t.slot = next
		s.requeue(t, next)
		return
	}
	if err != nil {
		s.finish(t, Failed)
	} else {
		s.finish(t, Completed)
	}
}

// requeue must be called with s.mu held.
func (s *Scheduler) requeue(t *ScheduledTask, at time.Time) {
	t.nextRun = at
	t.status = Scheduled
	heap.Push(&s.queue, t)
	s.notify()
}

// finish must be called with s.mu held. It records the final status and
// forgets the oldest finished task, so the tasks map can't grow forever.
func (s *Scheduler) finish(t *ScheduledTask, status Status) {
	t.status = status
	s.finished = append(s.finished, t.ID)
	if len(s.finished) > s.maxFinished {
		delete(s.tasks, s.finished[0])
		s.finished = s.finished[1:]
	}
}

// safeExecute turns a panicking task into an error so one bad task
// can't kill a worker goroutine.
func safeExecute(ctx context.Context, task Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("task panicked: %v", r)
		}
	}()
	return task.Execute(ctx)
}
