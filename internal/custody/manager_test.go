package custody

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SethDS/am-proxy/internal/config"
	"github.com/SethDS/am-proxy/internal/model"
)

func testCfg(urls []string, maxQueue int) *config.Config {
	cfg := config.Defaults()
	cfg.Queue.MaxAlertsPerBackend = maxQueue
	cfg.Queue.WorkerCount = 2
	cfg.Queue.OutboundBatchSize = 10
	cfg.Queue.RetryInitial = config.Duration(10 * time.Millisecond)
	cfg.Queue.RetryMax = config.Duration(50 * time.Millisecond)
	cfg.Queue.MaxRetries = 5
	cfg.Backends = map[string]config.Backend{
		"default": {URLs: urls, HAMode: "all", Timeout: config.Duration(time.Second)},
	}
	cfg.DefaultBackend = "default"
	cfg.Unmatched = "default"
	return &cfg
}

func TestEnqueueRejectionWhenFull(t *testing.T) {
	cfg := testCfg([]string{"http://127.0.0.1:1"}, 2)
	// Prevent workers from draining: do not Start.
	m := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.EnsureDestination(DestSpec{Key: "default", Backend: "default", URLs: cfg.Backends["default"].URLs, HAMode: "all", Timeout: time.Second})

	alerts := []model.Alert{
		{Labels: map[string]string{"n": "1"}},
		{Labels: map[string]string{"n": "2"}},
	}
	if err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: alerts}}); err != nil {
		t.Fatal(err)
	}
	err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: []model.Alert{{Labels: map[string]string{"n": "3"}}}}})
	if err != ErrFull {
		t.Fatalf("want ErrFull, got %v", err)
	}
}

func TestForwardSuccessAnd400DeadLetter(t *testing.T) {
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var alerts []model.Alert
		_ = json.Unmarshal(body, &alerts)
		got.Add(int32(len(alerts)))
		if len(alerts) > 0 && alerts[0].Labels["bad"] == "1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testCfg([]string{srv.URL}, 100)
	m := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	okAlert := model.Alert{Labels: map[string]string{"ok": "1"}}
	if err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: []model.Alert{okAlert}}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && got.Load() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if got.Load() < 1 {
		t.Fatal("alert not forwarded")
	}

	bad := model.Alert{Labels: map[string]string{"bad": "1"}}
	if err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: []model.Alert{bad}}}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(m.DeadLetters()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(m.DeadLetters()) == 0 {
		t.Fatal("expected dead-letter for 400")
	}
}

func TestDropExpiredOnEnqueue(t *testing.T) {
	cfg := testCfg([]string{"http://127.0.0.1:1"}, 10)
	m := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.EnsureDestination(DestSpec{Key: "default", URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: time.Second})

	expired := model.Alert{EndsAt: time.Now().Add(-time.Minute)}
	if err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: []model.Alert{expired}}}); err != nil {
		t.Fatal(err)
	}
	if m.QueueDepth("default") != 0 {
		t.Fatalf("expired alert should not sit in queue, depth=%d", m.QueueDepth("default"))
	}
}

func TestFailurePolicyAuthHold404And429(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := int(status.Load())
		if code == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(code)
	}))
	defer srv.Close()

	cfg := testCfg([]string{srv.URL}, 100)
	cfg.Queue.Retry404Max = 2
	cfg.Queue.RetryInitial = config.Duration(5 * time.Millisecond)
	cfg.Queue.WorkerCount = 1
	m := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	waitUntil := func(pred func() bool) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if pred() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return pred()
	}

	// 401 → auth hold, removed from queue (no rapid retry).
	status.Store(http.StatusUnauthorized)
	if err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: []model.Alert{
		{Labels: map[string]string{"case": "auth"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(func() bool { return len(m.AuthHolds()) >= 1 && m.QueueDepth("default") == 0 }) {
		t.Fatalf("auth hold missing: holds=%d depth=%d", len(m.AuthHolds()), m.QueueDepth("default"))
	}

	// 404 → bounded retries then dead-letter.
	status.Store(http.StatusNotFound)
	if err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: []model.Alert{
		{Labels: map[string]string{"case": "missing"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(func() bool {
		for _, dl := range m.DeadLetters() {
			if dl.Alert.Labels["case"] == "missing" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("expected 404 dead-letter, got %+v", m.DeadLetters())
	}

	// 429 → remain in custody (held), not dead-lettered.
	status.Store(http.StatusTooManyRequests)
	beforeDL := len(m.DeadLetters())
	if err := m.EnqueueAllOrNothing([]GroupedAlerts{{DestKey: "default", Alerts: []model.Alert{
		{Labels: map[string]string{"case": "rate"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(func() bool { return m.QueueDepth("default") >= 1 }) {
		t.Fatal("expected rate-limited alert retained in queue")
	}
	time.Sleep(50 * time.Millisecond)
	if len(m.DeadLetters()) != beforeDL {
		t.Fatalf("429 should not dead-letter immediately, dead=%d", len(m.DeadLetters())-beforeDL)
	}
}
