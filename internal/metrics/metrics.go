package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var registerRuntimeOnce sync.Once

// RegisterRuntime ensures Go and process collectors are on the default registry.
// Safe to call multiple times. Ignores AlreadyRegistered (some client_golang
// builds already install these on the default registry).
func RegisterRuntime() {
	registerRuntimeOnce.Do(func() {
		registerIgnoreDup(collectors.NewGoCollector())
		registerIgnoreDup(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	})
}

func registerIgnoreDup(c prometheus.Collector) {
	if err := prometheus.Register(c); err != nil {
		if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return
		}
		panic(err)
	}
}

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
	AlertsHeld = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_alerts_held_total",
		Help: "Alerts temporarily held (e.g. rate-limited) before retry.",
	}, []string{"backend", "reason"})
	QueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "am_proxy_queue_depth",
		Help: "Current custody queue depth per destination.",
	}, []string{"backend"})
	QueueCapacity = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "am_proxy_queue_capacity",
		Help: "Configured custody queue capacity per destination.",
	}, []string{"backend"})
	EnqueueRejections = promauto.NewCounter(prometheus.CounterOpts{
		Name: "am_proxy_enqueue_rejections_total",
		Help: "Ingress requests rejected with 503 (queue full or max in-flight).",
	})
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_http_requests_total",
		Help: "Ingress POST /api/v2/alerts responses by status code.",
	}, []string{"code"})
	ForwardDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "am_proxy_forward_duration_seconds",
		Help:    "Forward POST latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"backend"})
	ForwardErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_forward_errors_total",
		Help: "Forward failures by reason class.",
	}, []string{"backend", "reason"})
	ForwardRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "am_proxy_forward_retries_total",
		Help: "Alerts requeued for another forward attempt.",
	}, []string{"backend", "reason"})
	InflightRequests = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "am_proxy_inflight_requests",
		Help: "In-flight ingress HTTP requests.",
	})
)
