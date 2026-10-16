package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	AlertsReceived = promauto.NewCounter(prometheus.CounterOpts{
		Name: "am_proxy_alerts_received_total",
		Help: "Alerts received on ingress.",
	})
	AlertsDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_alerts_dropped_total",
		Help: "Alerts deliberately dropped by rules or unmatched=drop.",
	}, []string{"rule"})
	AlertsEnqueued = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_alerts_enqueued_total",
		Help: "Alerts accepted into custody queues.",
	}, []string{"backend"})
	AlertsForwarded = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_alerts_forwarded_total",
		Help: "Alerts successfully forwarded to Alertmanager.",
	}, []string{"backend"})
	AlertsExpired = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_alerts_expired_total",
		Help: "Queued alerts dropped because endsAt elapsed.",
	}, []string{"backend"})
	AlertsDeadLetter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_alerts_dead_letter_total",
		Help: "Alerts moved to dead-letter (poison / max age / retry limit).",
	}, []string{"backend", "reason"})
	AlertsAuthHold = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_alerts_auth_hold_total",
		Help: "Alerts retained after 401/403 (ops recovery).",
	}, []string{"backend"})
	QueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "am_proxy_queue_depth",
		Help: "Current custody queue depth per destination.",
	}, []string{"backend"})
	EnqueueRejections = promauto.NewCounter(prometheus.CounterOpts{
		Name: "am_proxy_enqueue_rejections_total",
		Help: "Ingress requests rejected because custody queues were full.",
	})
	ForwardDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "am_proxy_forward_duration_seconds",
		Help:    "Forward POST latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"backend"})
	ForwardErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_forward_errors_total",
		Help: "Forward failures by reason class.",
	}, []string{"backend", "reason"})
	InflightRequests = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "am_proxy_inflight_requests",
		Help: "In-flight ingress HTTP requests.",
	})
)
