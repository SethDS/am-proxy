package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SethDS/am-proxy/internal/config"
	"github.com/SethDS/am-proxy/internal/custody"
	"github.com/SethDS/am-proxy/internal/match"
	"github.com/SethDS/am-proxy/internal/metrics"
	"github.com/SethDS/am-proxy/internal/model"
)

func TestAlertsCustodyAndDrop(t *testing.T) {
	var forwarded int
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var alerts []model.Alert
		_ = json.NewDecoder(r.Body).Decode(&alerts)
		forwarded += len(alerts)
		w.WriteHeader(http.StatusOK)
	}))
	defer am.Close()

	cfg := config.Defaults()
	cfg.Queue.WorkerCount = 2
	cfg.Queue.OutboundBatchSize = 50
	cfg.Queue.RetryInitial = config.Duration(5 * time.Millisecond)
	cfg.Backends = map[string]config.Backend{
		"default": {URLs: []string{am.URL}, HAMode: "all", Timeout: config.Duration(time.Second)},
	}
	cfg.DefaultBackend = "default"
	cfg.Rules = []config.Rule{{
		Match:  []config.Matcher{{Path: "labels.alertname", Op: "exact", Value: "Watchdog"}},
		Action: "drop",
	}}

	eng, err := match.NewEngine(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := custody.NewManager(&cfg, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	defer mgr.Stop()

	s := &Server{Log: log, Engine: eng, Custody: mgr, MaxInflight: 100}
	body, _ := json.Marshal([]model.Alert{
		{Labels: map[string]string{"alertname": "Watchdog"}},
		{Labels: map[string]string{"alertname": "HighCPU", "severity": "warning"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && forwarded < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if forwarded != 1 {
		t.Fatalf("forwarded=%d want 1 (Watchdog dropped)", forwarded)
	}
}

func Test400OnMalformedJSON(t *testing.T) {
	cfg := config.Defaults()
	cfg.Backends = map[string]config.Backend{
		"default": {URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: config.Duration(time.Millisecond)},
	}
	cfg.DefaultBackend = "default"
	eng, _ := match.NewEngine(&cfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := custody.NewManager(&cfg, log)
	s := &Server{Log: log, Engine: eng, Custody: mgr, MaxInflight: 100}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader([]byte(`{not-json`)))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func Test503WhenQueueFull(t *testing.T) {
	cfg := config.Defaults()
	cfg.Queue.MaxAlertsPerBackend = 1
	cfg.Queue.WorkerCount = 1
	cfg.Backends = map[string]config.Backend{
		"default": {URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: config.Duration(time.Millisecond)},
	}
	cfg.DefaultBackend = "default"
	cfg.Rules = nil

	eng, _ := match.NewEngine(&cfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := custody.NewManager(&cfg, log)
	// Do not start workers so queue stays full.
	mgr.EnsureDestination(custody.DestSpec{
		Key: "default", Backend: "default",
		URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: time.Millisecond,
	})

	s := &Server{Log: log, Engine: eng, Custody: mgr, MaxInflight: 100}
	h := s.Handler()

	post := func() int {
		body, _ := json.Marshal([]model.Alert{{Labels: map[string]string{"a": "1"}}})
		req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("first post=%d", code)
	}
	if code := post(); code != http.StatusServiceUnavailable {
		t.Fatalf("second post=%d want 503", code)
	}
}

func TestMetricsEndpointScrapable(t *testing.T) {
	metrics.RegisterRuntime()

	cfg := config.Defaults()
	cfg.Queue.MaxAlertsPerBackend = 50
	cfg.Backends = map[string]config.Backend{
		"default": {URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: config.Duration(time.Millisecond)},
	}
	cfg.DefaultBackend = "default"
	eng, err := match.NewEngine(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := custody.NewManager(&cfg, log)
	// Create queue so capacity/depth gauges are observed.
	mgr.EnsureDestination(custody.DestSpec{
		Key: "default", Backend: "default",
		URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: time.Millisecond,
	})

	s := &Server{Log: log, Engine: eng, Custody: mgr, MaxInflight: 100}
	h := s.Handler()

	// Touch HTTP counter so the vec appears in exposition.
	body, _ := json.Marshal([]model.Alert{{Labels: map[string]string{"alertname": "x"}}})
	post := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(body))
	prr := httptest.NewRecorder()
	h.ServeHTTP(prr, post)
	if prr.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", prr.Code, prr.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	text := rr.Body.String()
	for _, want := range []string{
		"am_proxy_alerts_received_total",
		"am_proxy_queue_depth",
		"am_proxy_queue_capacity",
		`am_proxy_http_requests_total{code="200"}`,
		"go_goroutines",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics body missing %q", want)
		}
	}
}

func TestMetricsCountersMoveOnIngress(t *testing.T) {
	cfg := config.Defaults()
	cfg.Queue.MaxAlertsPerBackend = 10
	cfg.Backends = map[string]config.Backend{
		"default": {URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: config.Duration(time.Millisecond)},
	}
	cfg.DefaultBackend = "default"
	cfg.Rules = []config.Rule{{
		Match:  []config.Matcher{{Path: "labels.alertname", Op: "exact", Value: "Watchdog"}},
		Action: "drop",
	}}

	eng, err := match.NewEngine(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := custody.NewManager(&cfg, log)
	// No workers: keep alerts queued so enqueue path is deterministic.
	mgr.EnsureDestination(custody.DestSpec{
		Key: "default", Backend: "default",
		URLs: []string{"http://127.0.0.1:1"}, HAMode: "all", Timeout: time.Millisecond,
	})

	s := &Server{Log: log, Engine: eng, Custody: mgr, MaxInflight: 100}
	h := s.Handler()

	beforeReceived := testutil.ToFloat64(metrics.AlertsReceived)
	beforeDropped := testutil.ToFloat64(metrics.AlertsDropped.WithLabelValues("0"))
	beforeEnqueued := testutil.ToFloat64(metrics.AlertsEnqueued.WithLabelValues("default"))
	beforeHTTP200 := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("200"))
	beforeCapacity := testutil.ToFloat64(metrics.QueueCapacity.WithLabelValues("default"))

	if beforeCapacity != 10 {
		t.Fatalf("queue capacity gauge=%v want 10", beforeCapacity)
	}

	body, _ := json.Marshal([]model.Alert{
		{Labels: map[string]string{"alertname": "Watchdog"}},
		{Labels: map[string]string{"alertname": "HighCPU"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	if got := testutil.ToFloat64(metrics.AlertsReceived); got != beforeReceived+2 {
		t.Fatalf("alerts_received=%v want %v", got, beforeReceived+2)
	}
	if got := testutil.ToFloat64(metrics.AlertsDropped.WithLabelValues("0")); got != beforeDropped+1 {
		t.Fatalf("alerts_dropped=%v want %v", got, beforeDropped+1)
	}
	if got := testutil.ToFloat64(metrics.AlertsEnqueued.WithLabelValues("default")); got != beforeEnqueued+1 {
		t.Fatalf("alerts_enqueued=%v want %v", got, beforeEnqueued+1)
	}
	if got := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("200")); got != beforeHTTP200+1 {
		t.Fatalf("http_requests{code=200}=%v want %v", got, beforeHTTP200+1)
	}

	mreq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	mrr := httptest.NewRecorder()
	h.ServeHTTP(mrr, mreq)
	if mrr.Code != http.StatusOK {
		t.Fatalf("/metrics status=%d", mrr.Code)
	}
	text := mrr.Body.String()
	if !strings.Contains(text, "am_proxy_alerts_received_total") {
		t.Fatal("scraped metrics missing am_proxy_alerts_received_total")
	}
	if !strings.Contains(text, `am_proxy_http_requests_total{code="200"}`) {
		t.Fatal("scraped metrics missing http_requests code=200 sample")
	}
}
