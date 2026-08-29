package observability

import (
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var uuidPathWithSlash = regexp.MustCompile(`/[0-9a-fA-F]{8}-[0-9a-fA-F-]{27,}/`)
var uuidPathAtEnd = regexp.MustCompile(`/[0-9a-fA-F]{8}-[0-9a-fA-F-]{27,}$`)

type Metrics struct {
	Requests       *prometheus.CounterVec
	Duration       *prometheus.HistogramVec
	Errors         *prometheus.CounterVec
	WebhookReceipts *prometheus.CounterVec
	registry       *prometheus.Registry
}

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	metrics := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "limiance_http_requests_total", Help: "Total HTTP requests handled by the API."}, []string{"method", "path", "status"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "limiance_http_request_duration_seconds", Help: "HTTP request duration in seconds.", Buckets: prometheus.DefBuckets}, []string{"method", "path"}),
		Errors: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "limiance_http_errors_total", Help: "Total HTTP errors emitted by the API."}, []string{"method", "path", "status"}),
		WebhookReceipts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "limiance_webhook_receipts_total", Help: "Webhook receipt count by provider and result."}, []string{"provider", "status"}),
		registry: registry,
	}
	registry.MustRegister(metrics.Requests, metrics.Duration, metrics.Errors, metrics.WebhookReceipts)
	return metrics
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(writer, r)
		path := normalizePath(r.URL.Path)
		status := strconv.Itoa(writer.status)
		m.Requests.WithLabelValues(r.Method, path, status).Inc()
		m.Duration.WithLabelValues(r.Method, path).Observe(time.Since(started).Seconds())
		if writer.status >= http.StatusBadRequest {
			m.Errors.WithLabelValues(r.Method, path, status).Inc()
		}
	})
}

func (m *Metrics) RecordWebhookReceipt(provider, status string) {
	m.WebhookReceipts.WithLabelValues(provider, status).Inc()
}

func normalizePath(path string) string {
	path = uuidPathWithSlash.ReplaceAllString(path, "/:id/")
	return uuidPathAtEnd.ReplaceAllString(path, "/:id")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == http.StatusOK {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}
