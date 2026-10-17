package custody

import (
	"context"
	"time"

	"github.com/SethDS/am-proxy/internal/forward"
	"github.com/SethDS/am-proxy/internal/metrics"
	"github.com/SethDS/am-proxy/internal/model"
)

func (m *Manager) workerLoop(ctx context.Context, key string) {
	batchSize := m.cfg.Queue.OutboundBatchSize
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.mu.Lock()
		q := m.queues[key]
		spec := m.specs[key]
		m.mu.Unlock()
		if q == nil {
			return
		}

		batch, ok := q.popBatch(batchSize, time.Now())
		if !ok {
			return
		}
		if len(batch) == 0 {
			continue
		}

		// Drop expired again just before send.
		now := time.Now()
		alive := batch[:0]
		for _, it := range batch {
			if it.Alert.IsExpired(now) {
				metrics.AlertsExpired.WithLabelValues(key).Inc()
				continue
			}
			alive = append(alive, it)
		}
		if len(alive) == 0 {
			continue
		}

		m.forwardBatch(ctx, key, spec, alive)
	}
}

func (m *Manager) forwardBatch(ctx context.Context, key string, spec DestSpec, batch []Item) {
	alerts := make([]model.Alert, len(batch))
	for i := range batch {
		alerts[i] = batch[i].Alert
	}

	client := m.client
	if spec.Timeout > 0 {
		client = forward.NewClient(spec.Timeout)
	}

	urls := spec.URLs
	if len(urls) == 0 {
		m.deadLetterBatch(key, batch, "no_urls", 0)
		return
	}

	// ha_mode: all — POST every peer; any failure applies policy.
	// ha_mode: any — succeed if at least one peer accepts.
	haAll := spec.HAMode != "any"

	var last forward.Result
	success := false
	for _, u := range urls {
		start := time.Now()
		res := client.PostAlerts(ctx, u, alerts)
		metrics.ForwardDuration.WithLabelValues(key).Observe(time.Since(start).Seconds())
		last = res
		if res.Outcome == forward.OutcomeOK {
			success = true
			if !haAll {
				break
			}
			continue
		}
		if haAll {
			// Fail the batch for this attempt according to policy.
			m.applyFailure(key, batch, res)
			return
		}
	}
	if success {
		metrics.AlertsForwarded.WithLabelValues(key).Add(float64(len(batch)))
		return
	}
	m.applyFailure(key, batch, last)
}

func (m *Manager) applyFailure(key string, batch []Item, res forward.Result) {
	metrics.ForwardErrors.WithLabelValues(key, res.Outcome.String()).Inc()

	switch res.Outcome {
	case forward.OutcomeBadRequest, forward.OutcomeOther4xx:
		m.deadLetterBatch(key, batch, res.Outcome.String(), res.StatusCode)
		return

	case forward.OutcomeAuth:
		// Stop rapid retries; retain for ops recovery; alert ops via log.
		m.log.Error("alertmanager auth failure; retaining alerts for recovery",
			"dest", key, "status", res.StatusCode, "count", len(batch))
		now := time.Now()
		m.mu.Lock()
		for _, it := range batch {
			m.retainAuth(DeadLetter{
				DestKey: key,
				Alert:   it.Alert,
				Reason:  "auth",
				At:      now,
				Status:  res.StatusCode,
			})
		}
		m.mu.Unlock()
		return

	case forward.OutcomeNotFound:
		m.requeueWithPolicy(key, batch, func(it *Item) (keep bool) {
			it.NotFoundTries++
			it.Attempts++
			if it.NotFoundTries > m.cfg.Queue.Retry404Max {
				return false
			}
			it.NextAttempt = time.Now().Add(m.backoff(it.Attempts))
			return true
		}, "not_found", res.StatusCode)
		return

	case forward.OutcomeRateLimited:
		delay := res.RetryAfter
		if delay <= 0 {
			delay = m.cfg.Queue.RetryInitial.Duration()
		}
		until := time.Now().Add(delay)
		for i := range batch {
			batch[i].Attempts++
			batch[i].HoldUntil = until
		}
		metrics.AlertsHeld.WithLabelValues(key, "rate_limited").Add(float64(len(batch)))
		metrics.ForwardRetries.WithLabelValues(key, "rate_limited").Add(float64(len(batch)))
		if q := m.queue(key); q != nil {
			q.requeue(batch)
		}
		return

	case forward.OutcomeTimeout, forward.OutcomeRetryable:
		m.requeueWithPolicy(key, batch, func(it *Item) bool {
			it.Attempts++
			if it.Attempts > m.cfg.Queue.MaxRetries {
				return false
			}
			if time.Since(it.EnqueuedAt) > m.cfg.Queue.MaxAlertAge.Duration() {
				return false
			}
			it.NextAttempt = time.Now().Add(m.backoff(it.Attempts))
			return true
		}, res.Outcome.String(), res.StatusCode)
		return

	default:
		m.requeueWithPolicy(key, batch, func(it *Item) bool {
			it.Attempts++
			if it.Attempts > m.cfg.Queue.MaxRetries {
				return false
			}
			it.NextAttempt = time.Now().Add(m.backoff(it.Attempts))
			return true
		}, "unknown", res.StatusCode)
	}
}

func (m *Manager) requeueWithPolicy(key string, batch []Item, mutate func(*Item) bool, reason string, status int) {
	var again []Item
	var dead []Item
	for i := range batch {
		it := batch[i]
		if mutate(&it) {
			again = append(again, it)
		} else {
			dead = append(dead, it)
		}
	}
	if len(dead) > 0 {
		m.deadLetterBatch(key, dead, reason+"_limit", status)
	}
	if len(again) > 0 {
		metrics.ForwardRetries.WithLabelValues(key, reason).Add(float64(len(again)))
		if q := m.queue(key); q != nil {
			q.requeue(again)
		}
	}
}

func (m *Manager) deadLetterBatch(key string, batch []Item, reason string, status int) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range batch {
		m.retainDead(DeadLetter{
			DestKey: key,
			Alert:   it.Alert,
			Reason:  reason,
			At:      now,
			Status:  status,
		})
	}
	m.log.Error("alerts moved to dead-letter",
		"dest", key, "reason", reason, "status", status, "count", len(batch))
}

func (m *Manager) queue(key string) *queue {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queues[key]
}

func (m *Manager) backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := m.cfg.Queue.RetryInitial.Duration()
	max := m.cfg.Queue.RetryMax.Duration()
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= max {
			d = max
			break
		}
	}
	// Full jitter: [0, d]
	m.mu.Lock()
	j := time.Duration(m.rng.Int63n(int64(d) + 1))
	m.mu.Unlock()
	return j
}
