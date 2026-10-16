package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SethDS/am-proxy/internal/config"
	"github.com/SethDS/am-proxy/internal/custody"
	"github.com/SethDS/am-proxy/internal/match"
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
