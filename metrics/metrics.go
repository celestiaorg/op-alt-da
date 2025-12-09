// Package metrics provides Prometheus metrics for the stateless DA server.
// This is a simplified version without batch-related metrics.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CelestiaMetrics holds all metrics for the stateless DA server.
type CelestiaMetrics struct {
	// HTTP-level metrics (compatible with main branch dashboards)
	RequestDuration *prometheus.HistogramVec // method: "get", "put"
	BlobSize        prometheus.Histogram
	InclusionHeight prometheus.Gauge

	// Celestia submission metrics
	SubmissionDuration prometheus.Histogram
	SubmissionsTotal   prometheus.Counter
	SubmissionErrors   prometheus.Counter

	// Celestia retrieval metrics
	RetrievalDuration prometheus.Histogram
	RetrievalsTotal   prometheus.Counter
	RetrievalErrors   prometheus.Counter

	// Fallback metrics
	FallbackWritesTotal prometheus.Counter
	FallbackWriteErrors prometheus.Counter
}

// NewCelestiaMetrics creates a new CelestiaMetrics instance registered with the given registry.
func NewCelestiaMetrics(registry prometheus.Registerer) *CelestiaMetrics {
	factory := promauto.With(registry)

	return &CelestiaMetrics{
		RequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "op_altda_request_duration_seconds",
			Help:    "Duration of HTTP requests",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),

		BlobSize: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "op_altda_blob_size_bytes",
			Help:    "Size of blobs in bytes",
			Buckets: []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304},
		}),

		InclusionHeight: factory.NewGauge(prometheus.GaugeOpts{
			Name: "op_altda_inclusion_height",
			Help: "Latest Celestia inclusion height",
		}),

		SubmissionDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "celestia_submission_duration_seconds",
			Help:    "Time taken to submit blob to Celestia",
			Buckets: []float64{0.5, 1, 2, 5, 10, 15, 30, 60, 120},
		}),

		SubmissionsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_submissions_total",
			Help: "Total number of blob submissions to Celestia",
		}),

		SubmissionErrors: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_submission_errors_total",
			Help: "Total number of failed blob submissions",
		}),

		RetrievalDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "celestia_retrieval_duration_seconds",
			Help:    "Time taken to retrieve blob from Celestia",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30},
		}),

		RetrievalsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_retrievals_total",
			Help: "Total number of blob retrievals from Celestia",
		}),

		RetrievalErrors: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_retrieval_errors_total",
			Help: "Total number of failed blob retrievals",
		}),

		FallbackWritesTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_fallback_writes_total",
			Help: "Total number of fallback write attempts",
		}),

		FallbackWriteErrors: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_fallback_write_errors_total",
			Help: "Total number of failed fallback writes",
		}),
	}
}

// RecordHTTPRequest records the duration of an HTTP request.
func (m *CelestiaMetrics) RecordHTTPRequest(method string, duration time.Duration) {
	m.RequestDuration.WithLabelValues(method).Observe(duration.Seconds())
}

// RecordBlobSize records the size of a blob.
func (m *CelestiaMetrics) RecordBlobSize(size int) {
	m.BlobSize.Observe(float64(size))
}

// SetInclusionHeight sets the latest Celestia inclusion height.
func (m *CelestiaMetrics) SetInclusionHeight(height uint64) {
	m.InclusionHeight.Set(float64(height))
}

// RecordSubmission records a successful blob submission to Celestia.
func (m *CelestiaMetrics) RecordSubmission(duration time.Duration, size int) {
	m.SubmissionDuration.Observe(duration.Seconds())
	m.SubmissionsTotal.Inc()
	m.BlobSize.Observe(float64(size))
}

// RecordSubmissionError records a failed blob submission.
func (m *CelestiaMetrics) RecordSubmissionError() {
	m.SubmissionErrors.Inc()
}

// RecordRetrieval records a successful blob retrieval from Celestia.
func (m *CelestiaMetrics) RecordRetrieval(duration time.Duration, size int) {
	m.RetrievalDuration.Observe(duration.Seconds())
	m.RetrievalsTotal.Inc()
}

// RecordRetrievalError records a failed blob retrieval.
func (m *CelestiaMetrics) RecordRetrievalError() {
	m.RetrievalErrors.Inc()
}

// RecordFallbackWrite records a successful fallback write.
func (m *CelestiaMetrics) RecordFallbackWrite() {
	m.FallbackWritesTotal.Inc()
}

// RecordFallbackWriteError records a failed fallback write.
func (m *CelestiaMetrics) RecordFallbackWriteError() {
	m.FallbackWritesTotal.Inc()
	m.FallbackWriteErrors.Inc()
}

