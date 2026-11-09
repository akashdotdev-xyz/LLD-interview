package scheduler

import "sync"

// WorkerPool runs submitted jobs on a fixed number of goroutines.
// It knows nothing about schedules or retries, it only executes what it's given.
type WorkerPool struct {
	size     int
	jobs     chan func()
	quit     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func NewWorkerPool(size int) *WorkerPool {
	if size <= 0 {
		panic("worker pool size must be positive") // with no workers, nothing would ever run
	}
	return &WorkerPool{
		size: size,
		jobs: make(chan func()),
		quit: make(chan struct{}),
	}
}

func (p *WorkerPool) Start() {
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.work()
	}
}

// Submit blocks until a worker takes the job. It returns false if the pool
// was stopped, in which case the job will not run.
func (p *WorkerPool) Submit(job func()) bool {
	select {
	case p.jobs <- job:
		return true
	case <-p.quit:
		return false
	}
}

// Stop waits for running jobs to finish. jobs is never closed because Submit
// may be sending at the same time, and sending on a closed channel panics.
func (p *WorkerPool) Stop() {
	p.stopOnce.Do(func() {
		close(p.quit)
		p.wg.Wait()
	})
}

func (p *WorkerPool) work() {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs:
			job()
		case <-p.quit:
			return
		}
	}
}
