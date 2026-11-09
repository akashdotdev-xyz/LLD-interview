# Task Scheduler: LLD Interview Walkthrough

Run it:
```
go run ./cmd                 # demo
go test -race ./...          # tests
```

---

## Step 1: Clarify requirements (≈5 min)

Always ask questions first. Interviewers grade this step.

**Functional**
1. Schedule a task to run **once** after a delay or at a given time.
2. Schedule **recurring** tasks:
   - *fixed rate*: every N sec from the scheduled start (10:00, 10:05, 10:10…)
   - *fixed delay*: N sec after the previous run **finishes**
3. **Cancel** a task.
4. Query a task's **status**.
5. Run tasks **concurrently** with a bounded number of workers.
6. (Good extras) **retry** on failure, **priority** to break ties.

**Non-functional**
- Tasks run on time (no busy polling, no drift from slow tasks).
- Thread-safe: `Schedule` and `Cancel` can be called from any goroutine.
- One failing or panicking task must not break the scheduler.
- Single process, in memory (say what you're *not* building: persistence, distributed workers).

> Tip: say "This is similar to Java's `ScheduledExecutorService`", because interviewers recognise it.

---

## Step 2: Core entities (≈5 min)

| Entity | Responsibility |
|---|---|
| `Task` (interface) | The work: `Execute(ctx) error`. **Command pattern** |
| `Schedule` (interface) | Decides the next run time: `OneTime`, `FixedRate`, `FixedDelay`. **Strategy pattern** |
| `ScheduledTask` | Task + schedule + `nextRun`, `status`, retry info, heap index |
| `TaskQueue` | Min-heap ordered by `nextRun` (tie-break: priority) |
| `Scheduler` | Public API, owns the heap and the dispatcher; decides what runs and when |
| `WorkerPool` | Runs submitted functions on N goroutines; knows nothing about schedules |
| `Status` | `SCHEDULED → RUNNING → (SCHEDULED again) / COMPLETED / FAILED / CANCELLED` |

```
        ┌────────────┐ uses  ┌──────────────┐
        │ Scheduler  │──────▶│  TaskQueue   │ (min-heap)
        └─────┬──────┘       └──────┬───────┘
              │ map[id]             │ holds
              ▼                     ▼
        ┌─────────────────────────────────┐
        │ ScheduledTask                   │
        │  ID, nextRun, status, priority  │
        │  attempts, retry RetryPolicy    │
        └──────┬──────────────────┬───────┘
               │                  │
        ┌──────▼─────┐     ┌──────▼──────────────┐
        │ «Task»     │     │ «Schedule»          │
        │ Execute()  │     │ Next(sched, fin)    │
        └────────────┘     ├─────────────────────┤
                           │ OneTime             │
                           │ FixedRate           │
                           │ FixedDelay          │
                           │ (Cron: extension)   │
                           └─────────────────────┘
```

---

## Step 3: The key design decision (the core of the interview)

**How do we know when a task is due?**

| Approach | Problem |
|---|---|
| Loop over all tasks every 100ms | Wastes CPU, O(n) scan, up to 100ms late |
| One goroutine/thread per task that sleeps | 1M tasks means 1M sleeping threads; no limit on concurrency |
| **Min-heap + one dispatcher + worker pool** ✅ | O(log n) insert, O(1) "what's next?", bounded concurrency |

**How it works** (see [scheduler.go](internal/scheduler/scheduler.go)):

```
Schedule() ──push──▶ [ min-heap by nextRun ] ◀──reschedule── run()
                            │
               dispatcher sleeps until heap top is due
                            │
                            ▼
                 pool.Submit(run) ──▶ worker 1..N ──▶ task.Execute()
```

1. **Dispatcher** peeks at the heap top. If it's due, it pops it and submits it to the pool.
   Otherwise it sleeps on a timer set to `top.nextRun - now`.
2. **The wake-up problem:** the dispatcher sleeps for 10 min, then someone schedules a task
   due in 1 sec. The dispatcher must wake up. So `Schedule`/`Cancel` send a non-blocking signal
   on a `wake` channel (buffer of 1), and the dispatcher recalculates.
   (In Java this is `Condition.awaitNanos()` + `signal()`, which is exactly how `DelayQueue` works.)
3. **`WorkerPool`** executes the function it was given, which is `Scheduler.run(task)`. `run` executes the task,
   then asks `Schedule.Next()` for the next time and pushes it back into the heap.
   One-time tasks return `ok=false`, so they're marked completed.

---

## Step 4: Concurrency pitfalls (what separates good answers)

1. **Don't hold the lock while handing off to workers.** If all workers are busy, `Submit` blocks.
   Workers need the same lock to reschedule, so you get a **deadlock**. `popDue()` collects due tasks under the lock,
   then the dispatcher submits them after unlocking.
2. **Cancel when the task isn't in the heap.** There are two cases, and both mark it `CANCELLED`.
   If it's waiting for a free worker, `run` checks the status first and skips it. If it's already running,
   the worker sees the status when it finishes and doesn't reschedule.
3. **O(log n) cancel.** Each `ScheduledTask` stores its heap `index` (kept up to date in `Swap`),
   so `heap.Remove(index)` works. Alternative: *lazy delete*, which marks it cancelled and skips it when popped.
   That's simpler but leaves junk in the heap.
4. **Panics.** `safeExecute` recovers and turns a panic into an error, so a worker goroutine never dies.
5. **Graceful shutdown.** `Stop()` cancels the `ctx` that the dispatcher and tasks watch. `pool.Stop()` waits for
   running jobs and makes any blocked `Submit` return false, so the dispatcher exits too. The pool's `jobs` channel
   is never closed, because `Submit` could be sending on it at that moment, and that would panic. Tasks that never
   started are marked `CANCELLED`, and scheduling after `Stop` returns `ErrStopped`.
6. **Fixed rate overrun.** If a run every 1s takes 3s, don't fire 3 catch-up runs in a burst.
   `FixedRate.Next` skips ahead to the next future slot. A zero interval would make that loop spin forever,
   so it's rejected up front.
7. **Retries vs fixed rate.** A retry changes `nextRun` but not `slot`, so the next regular run still lands on the
   original schedule.
8. **Unbounded memory.** Finished tasks are needed for `Status` but can't be kept forever, so only the last
   1000 are kept.

---

## Step 5: Design patterns used (name them out loud)

- **Command**: `Task` hides what the work is.
- **Strategy**: `Schedule`. Adding a cron schedule means adding a new type, with no change to `Scheduler` (Open/Closed).
- **Producer–Consumer**: dispatcher → `WorkerPool.Submit` → workers. Separating the pool gives each type one job (SRP).
- **Functional options**: `WithRetry`, `WithPriority` keep the API small while staying extensible.
- (If asked) **Observer**: add `OnComplete/OnFail` listeners for metrics and alerts.

---

## Step 6: Follow-up questions to prepare

| Question | Answer sketch |
|---|---|
| Add cron expressions? | New `CronSchedule` that implements `Schedule.Next`. Nothing else changes. |
| Task dependencies (B after A)? | DAG: keep an in-degree count per task; when A completes, decrement its children's counts and push any that reach 0 into the heap. |
| Survive restarts? | Persist tasks (DB table with `next_run`, status). On startup, load them into the heap. Make tasks idempotent. |
| Distributed / multiple machines? | DB-backed queue, workers claim with `SELECT … FOR UPDATE SKIP LOCKED` or a lease with a TTL; or Redis sorted set (score = nextRun). Needs at-least-once delivery plus idempotency. |
| Millions of timers? | **Hierarchical timing wheel** (Kafka, Netty): O(1) insert vs O(log n) heap. |
| Timeout per task? | `context.WithTimeout` around `Execute`. |
| Exponential backoff? | `Backoff * 2^attempts` (+ jitter) in `run()`. |
| Rate-limit a task type? | Separate worker pools per type, or a semaphore per type. |

---

## Practice plan

1. Read the code in this order: `task.go` → `schedule.go` → `task_queue.go` → `worker_pool.go` → `scheduler.go`.
2. Close the files. Re-write it from scratch in **≤ 45 min** (start with one-time tasks only, then add recurring, then cancel).
3. Explain Step 3 and Step 4 out loud, as if to an interviewer.
4. Pick 2 follow-ups and implement them (cron and DAG dependencies are the most common).
