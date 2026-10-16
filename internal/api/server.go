package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/SethDS/am-proxy/internal/custody"
	"github.com/SethDS/am-proxy/internal/match"
	"github.com/SethDS/am-proxy/internal/metrics"
	"github.com/SethDS/am-proxy/internal/model"
)

type Server struct {
	Log            *slog.Logger
	Engine         *match.Engine
	Custody        *custody.Manager
	MaxInflight    int
	inflight       atomic.Int64
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/alerts", s.handleAlerts)
	mux.HandleFunc("GET /-/healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("GET /-/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Ready"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	n := s.inflight.Add(1)
	metrics.InflightRequests.Set(float64(n))
	defer func() {
		metrics.InflightRequests.Set(float64(s.inflight.Add(-1)))
	}()

	if s.MaxInflight > 0 && int(n) > s.MaxInflight {
		metrics.EnqueueRejections.Inc()
		http.Error(w, "too many in-flight requests", http.StatusServiceUnavailable)
		return
	}

	var alerts []model.Alert
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&alerts); err != nil {
		http.Error(w, "invalid alert payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	metrics.AlertsReceived.Add(float64(len(alerts)))

	grouped := map[string][]model.Alert{}
	var destOrder []string

	for _, a := range alerts {
		d := s.Engine.Evaluate(a)
		if d.Drop {
			rule := "unmatched"
			if d.Rule >= 0 {
				rule = strconv.Itoa(d.Rule)
			}
			metrics.AlertsDropped.WithLabelValues(rule).Inc()
			continue
		}
		for _, dest := range d.Dests {
			spec, err := s.Custody.ResolveDest(dest.Backend, dest.URL)
			if err != nil {
				s.Log.Error("resolve destination", "err", err)
				http.Error(w, "configuration error", http.StatusInternalServerError)
				return
			}
			s.Custody.EnsureDestination(spec)
			if _, ok := grouped[spec.Key]; !ok {
				destOrder = append(destOrder, spec.Key)
			}
			grouped[spec.Key] = append(grouped[spec.Key], a)
		}
	}

	groups := make([]custody.GroupedAlerts, 0, len(destOrder))
	for _, key := range destOrder {
		groups = append(groups, custody.GroupedAlerts{DestKey: key, Alerts: grouped[key]})
	}

	if err := s.Custody.EnqueueAllOrNothing(groups); err != nil {
		if errors.Is(err, custody.ErrFull) {
			http.Error(w, "custody queues full", http.StatusServiceUnavailable)
			return
		}
		s.Log.Error("enqueue failed", "err", err)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
