package custody

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/SethDS/am-proxy/internal/config"
	"github.com/SethDS/am-proxy/internal/forward"
	"github.com/SethDS/am-proxy/internal/metrics"
	"github.com/SethDS/am-proxy/internal/model"
)

var ErrFull = fmt.Errorf("custody queues full")

// Manager owns per-destination memory queues and forward workers.
type Manager struct {
	cfg    *config.Config
	log    *slog.Logger
	mu     sync.Mutex
	queues map[string]*queue
	specs  map[string]DestSpec
	started map[string]bool
	dead   []DeadLetter
	auth   []DeadLetter
	client *forward.Client
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
	rng    *rand.Rand
}

type DeadLetter struct {
	DestKey string
	Alert   model.Alert
	Reason  string
	At      time.Time
	Status  int
}

func NewManager(cfg *config.Config, log *slog.Logger) *Manager {
	return &Manager{
		cfg:     cfg,
		log:     log,
		queues:  make(map[string]*queue),
		specs:   make(map[string]DestSpec),
		started: make(map[string]bool),
		client:  forward.NewClient(5 * time.Second),
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (m *Manager) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	m.ctx = ctx
	m.cancel = cancel

	for name, b := range m.cfg.Backends {
		spec := DestSpec{
			Key:     name,
			Backend: name,
			URLs:    append([]string{}, b.URLs...),
			HAMode:  b.HAMode,
			Timeout: b.Timeout.Duration(),
		}
		m.EnsureDestination(spec)
	}
}

func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	for _, q := range m.queues {
		q.close()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// ResolveDest builds DestSpec for a named backend or inline URL.
func (m *Manager) ResolveDest(backend, url string) (DestSpec, error) {
	if url != "" {
		timeout := 5 * time.Second
		return DestSpec{
			Key:     "url:" + url,
			URLs:    []string{url},
			HAMode:  "all",
			Timeout: timeout,
		}, nil
	}
	b, ok := m.cfg.Backends[backend]
	if !ok {
		return DestSpec{}, fmt.Errorf("unknown backend %q", backend)
	}
	return DestSpec{
		Key:     backend,
		Backend: backend,
		URLs:    append([]string{}, b.URLs...),
		HAMode:  b.HAMode,
		Timeout: b.Timeout.Duration(),
	}, nil
}

// EnsureDestination creates a queue and starts workers if needed.
func (m *Manager) EnsureDestination(spec DestSpec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.specs[spec.Key] = spec
	if _, ok := m.queues[spec.Key]; !ok {
		m.queues[spec.Key] = newQueue(spec.Key, m.cfg.Queue.MaxAlertsPerBackend)
	}
	if m.ctx == nil {
		return
	}
	if m.started[spec.Key] {
		return
	}
	m.started[spec.Key] = true
	key := spec.Key
	n := m.cfg.Queue.WorkerCount
	for i := 0; i < n; i++ {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.workerLoop(m.ctx, key)
		}()
	}
}

// EnqueueAllOrNothing accepts custody for the whole request or fails with ErrFull.
func (m *Manager) EnqueueAllOrNothing(groups []GroupedAlerts) error {
	if len(groups) == 0 {
		return nil
	}
	now := time.Now()

	type pending struct {
		q     *queue
		items []Item
		key   string
	}
	var plan []pending

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, g := range groups {
		if len(g.Alerts) == 0 {
			continue
		}
		q, ok := m.queues[g.DestKey]
		if !ok {
			return fmt.Errorf("no queue for destination %q", g.DestKey)
		}
		items := make([]Item, 0, len(g.Alerts))
		for _, a := range g.Alerts {
			if a.IsExpired(now) {
				metrics.AlertsExpired.WithLabelValues(g.DestKey).Inc()
				continue
			}
			items = append(items, Item{Alert: a, EnqueuedAt: now})
		}
		if len(items) == 0 {
			continue
		}
		plan = append(plan, pending{q: q, items: items, key: g.DestKey})
	}

	for _, p := range plan {
		p.q.mu.Lock()
		full := p.q.closed || len(p.q.items)+len(p.items) > p.q.max
		p.q.mu.Unlock()
		if full {
			metrics.EnqueueRejections.Inc()
			return ErrFull
		}
	}
	for _, p := range plan {
		p.q.mu.Lock()
		p.q.pushLocked(p.items)
		p.q.mu.Unlock()
		metrics.AlertsEnqueued.WithLabelValues(p.key).Add(float64(len(p.items)))
	}
	return nil
}

func (m *Manager) QueueDepth(key string) int {
	m.mu.Lock()
	q := m.queues[key]
	m.mu.Unlock()
	if q == nil {
		return 0
	}
	return q.len()
}

func (m *Manager) DeadLetters() []DeadLetter {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DeadLetter, len(m.dead))
	copy(out, m.dead)
	return out
}

func (m *Manager) AuthHolds() []DeadLetter {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DeadLetter, len(m.auth))
	copy(out, m.auth)
	return out
}

func (m *Manager) retainDead(dl DeadLetter) {
	capN := m.cfg.Forward.DeadLetterCap
	m.dead = append(m.dead, dl)
	if len(m.dead) > capN {
		m.dead = m.dead[len(m.dead)-capN:]
	}
	metrics.AlertsDeadLetter.WithLabelValues(dl.DestKey, dl.Reason).Inc()
}

func (m *Manager) retainAuth(dl DeadLetter) {
	capN := m.cfg.Forward.DeadLetterCap
	m.auth = append(m.auth, dl)
	if len(m.auth) > capN {
		m.auth = m.auth[len(m.auth)-capN:]
	}
	metrics.AlertsAuthHold.WithLabelValues(dl.DestKey).Inc()
}
