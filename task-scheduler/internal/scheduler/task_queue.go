package scheduler

import "container/heap"

// TaskQueue is a min-heap ordered by nextRun (then priority).
// Peek is O(1), push/pop/remove are O(log n).
type TaskQueue []*ScheduledTask

var _ heap.Interface = (*TaskQueue)(nil)

func (q TaskQueue) Len() int { return len(q) }

func (q TaskQueue) Less(i, j int) bool {
	if q[i].nextRun.Equal(q[j].nextRun) {
		return q[i].priority > q[j].priority
	}
	return q[i].nextRun.Before(q[j].nextRun)
}

func (q TaskQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *TaskQueue) Push(x any) {
	t := x.(*ScheduledTask)
	t.index = len(*q)
	*q = append(*q, t)
}

func (q *TaskQueue) Pop() any {
	old := *q
	n := len(old)
	t := old[n-1]
	old[n-1] = nil
	t.index = -1
	*q = old[:n-1]
	return t
}

func (q TaskQueue) Peek() *ScheduledTask {
	if len(q) == 0 {
		return nil
	}
	return q[0]
}
