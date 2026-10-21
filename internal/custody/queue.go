package custody

import (
	"fmt"
	"sync"
	"time"

	"github.com/SethDS/am-proxy/internal/metrics"
	"github.com/SethDS/am-proxy/internal/model"
)

type queue struct {
	mu       sync.Mutex
	items    []Item
	max      int
	notEmpty *sync.Cond
	closed   bool
	destKey  string
}

func newQueue(destKey string, max int) *queue {
	q := &queue{max: max, destKey: destKey}
	q.notEmpty = sync.NewCond(&q.mu)
	metrics.QueueCapacity.WithLabelValues(destKey).Set(float64(max))
	metrics.QueueDepth.WithLabelValues(destKey).Set(0)
	return q
}

func (q *queue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.notEmpty.Broadcast()
	q.mu.Unlock()
}

// tryReserve checks capacity for n more items without mutating.
func (q *queue) tryReserve(n int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.closed && len(q.items)+n <= q.max
}

func (q *queue) pushLocked(items []Item) {
	q.items = append(q.items, items...)
	metrics.QueueDepth.WithLabelValues(q.destKey).Set(float64(len(q.items)))
	q.notEmpty.Broadcast()
}

func (q *queue) push(items []Item) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return fmt.Errorf("queue closed")
	}
	if len(q.items)+len(items) > q.max {
		return errFull
	}
	q.pushLocked(items)
	return nil
}

var errFull = fmt.Errorf("queue full")

// popBatch waits until work is available, then returns up to n ready items
// (NextAttempt/HoldUntil elapsed). Expired alerts are dropped.
func (q *queue) popBatch(n int, now time.Time) (ready []Item, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if q.closed && len(q.items) == 0 {
			return nil, false
		}
		ready, remaining, expired, wait := partitionReady(q.items, n, now)
		for range expired {
			metrics.AlertsExpired.WithLabelValues(q.destKey).Inc()
		}
		q.items = remaining
		metrics.QueueDepth.WithLabelValues(q.destKey).Set(float64(len(q.items)))
		if len(ready) > 0 {
			return ready, true
		}
		if q.closed {
			return nil, false
		}
		if wait > 0 {
			// Timed wait via cond: release and sleep outside would race; use Broadcast from ticker.
			timer := time.AfterFunc(wait, func() {
				q.mu.Lock()
				q.notEmpty.Broadcast()
				q.mu.Unlock()
			})
			q.notEmpty.Wait()
			timer.Stop()
			now = time.Now()
			continue
		}
		q.notEmpty.Wait()
		now = time.Now()
	}
}

func (q *queue) requeue(items []Item) {
	if len(items) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	// Prefer front so retries are not starved behind new traffic forever,
	// but keep order among the requeued batch.
	q.items = append(items, q.items...)
	metrics.QueueDepth.WithLabelValues(q.destKey).Set(float64(len(q.items)))
	q.notEmpty.Broadcast()
}

func partitionReady(items []Item, n int, now time.Time) (ready, remaining []Item, expired int, wait time.Duration) {
	wait = -1
	for _, it := range items {
		if it.Alert.IsExpired(now) {
			expired++
			continue
		}
		if !it.HoldUntil.IsZero() && it.HoldUntil.After(now) {
			d := it.HoldUntil.Sub(now)
			if wait < 0 || d < wait {
				wait = d
			}
			remaining = append(remaining, it)
			continue
		}
		if !it.NextAttempt.IsZero() && it.NextAttempt.After(now) {
			d := it.NextAttempt.Sub(now)
			if wait < 0 || d < wait {
				wait = d
			}
			remaining = append(remaining, it)
			continue
		}
		if len(ready) < n {
			ready = append(ready, it)
			continue
		}
		remaining = append(remaining, it)
	}
	if wait < 0 {
		wait = 0
	}
	return ready, remaining, expired, wait
}

// GroupedAlerts is a batch of alerts destined for one queue key.
type GroupedAlerts struct {
	DestKey string
	Alerts  []model.Alert
}
