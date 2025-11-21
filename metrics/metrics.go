package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CelestiaMetrics tracks performance metrics for Celestia DA operations
type CelestiaMetrics struct {
	// HTTP-level metrics (from main branch - compatible with existing dashboards)
	RequestDuration *prometheus.HistogramVec // method label: "get" or "put"
	BlobSize        prometheus.Histogram
	InclusionHeight prometheus.Gauge

	// GET metrics - Option A: Time to Availability (comparable to main branch blocking behavior)
	TimeToAvailability prometheus.Histogram // Time from first GET request to 200 OK

	// GET metrics - Option B: Cached retrieval speed (competitive advantage)
	GetRequestDuration *prometheus.HistogramVec // status label: "200", "404", "500"
	GetRequestsTotal   *prometheus.CounterVec   // status label: "200", "404", "500"

	// PUT metrics - Transparency about batching and DA confirmation
	TimeToBatch         prometheus.Histogram // Time from PUT to blob batched
	TimeToConfirmation  prometheus.Histogram // Time from PUT to DA confirmation

	// Worker-level metrics (more detailed operational metrics)
	SubmissionDuration prometheus.Histogram
	SubmissionSize     prometheus.Histogram
	SubmissionsTotal   prometheus.Counter
	SubmissionErrors   prometheus.Counter

	RetrievalDuration prometheus.Histogram
	RetrievalSize     prometheus.Histogram
	RetrievalsTotal   prometheus.Counter
	RetrievalErrors   prometheus.Counter
}

// NewCelestiaMetrics creates a new metrics collector for Celestia operations
func NewCelestiaMetrics(registry prometheus.Registerer) *CelestiaMetrics {
	if registry == nil {
		registry = prometheus.NewRegistry()
	}

	factory := promauto.With(registry)

	return &CelestiaMetrics{
		// HTTP-level metrics (from main branch)
		RequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "op_altda_request_duration_seconds",
			Help:    "Duration of HTTP requests",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		BlobSize: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "op_altda_blob_size_bytes",
			Help:    "Size of blobs in bytes",
			Buckets: []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 8388608}, // 1k to 8M
		}),
		InclusionHeight: factory.NewGauge(prometheus.GaugeOpts{
			Name: "op_altda_inclusion_height",
			Help: "Inclusion height of blobs in Celestia",
		}),

		// GET Option A: Time to Availability (comparable to main branch)
		TimeToAvailability: factory.NewHistogram(prometheus.HistogramOpts{
			Name: "op_altda_time_to_availability_seconds",
			Help: "Time from first GET request to 200 OK response (comparable to main branch blocking behavior)",
			Buckets: []float64{
				1, 5, 10, 15, 20, 30, 45, 60, // seconds
				90, 120, 180, 300, 600, // up to 10 minutes
			},
		}),

		// GET Option B: Cached retrieval performance
		GetRequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name: "op_altda_get_request_duration_seconds",
			Help: "Duration of GET requests by status code (shows cached retrieval speed for 200s)",
			Buckets: []float64{
				0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, // sub-second to seconds
			},
		}, []string{"status"}),
		GetRequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "op_altda_get_requests_total",
			Help: "Total GET requests by status code",
		}, []string{"status"}),

		// PUT transparency metrics
		TimeToBatch: factory.NewHistogram(prometheus.HistogramOpts{
			Name: "op_altda_time_to_batch_seconds",
			Help: "Time from PUT request to blob being batched",
			Buckets: []float64{
				1, 5, 10, 15, 20, 30, 45, 60, 90, 120, // seconds
			},
		}),
		TimeToConfirmation: factory.NewHistogram(prometheus.HistogramOpts{
			Name: "op_altda_time_to_confirmation_seconds",
			Help: "Time from PUT request to DA layer confirmation",
			Buckets: []float64{
				10, 20, 30, 45, 60, 90, 120, 180, 300, 600, 900, 1800, // seconds to 30 minutes
			},
		}),

		// Worker-level submission metrics
		SubmissionDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name: "celestia_submission_duration_seconds",
			Help: "Time taken to submit batch to Celestia (worker perspective)",
			Buckets: []float64{
				0.1, 0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, // seconds
			},
		}),
		SubmissionSize: factory.NewHistogram(prometheus.HistogramOpts{
			Name: "celestia_submission_size_bytes",
			Help: "Size of data submitted to Celestia in bytes",
			Buckets: []float64{
				1024,           // 1 KB
				10 * 1024,      // 10 KB
				100 * 1024,     // 100 KB
				500 * 1024,     // 500 KB
				1024 * 1024,    // 1 MB
				5 * 1024 * 1024, // 5 MB
				10 * 1024 * 1024, // 10 MB
			},
		}),
		SubmissionsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_submissions_total",
			Help: "Total number of batch submissions to Celestia",
		}),
		SubmissionErrors: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_submission_errors_total",
			Help: "Total number of failed batch submissions to Celestia",
		}),

		// Retrieval metrics
		RetrievalDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name: "celestia_retrieval_duration_seconds",
			Help: "Time taken to retrieve blob from Celestia",
			Buckets: []float64{
				0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30, 60, // seconds
			},
		}),
		RetrievalSize: factory.NewHistogram(prometheus.HistogramOpts{
			Name: "celestia_retrieval_size_bytes",
			Help: "Size of data retrieved from Celestia in bytes",
			Buckets: []float64{
				1024,           // 1 KB
				10 * 1024,      // 10 KB
				100 * 1024,     // 100 KB
				500 * 1024,     // 500 KB
				1024 * 1024,    // 1 MB
				5 * 1024 * 1024, // 5 MB
				10 * 1024 * 1024, // 10 MB
			},
		}),
		RetrievalsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_retrievals_total",
			Help: "Total number of blob retrievals from Celestia",
		}),
		RetrievalErrors: factory.NewCounter(prometheus.CounterOpts{
			Name: "celestia_retrieval_errors_total",
			Help: "Total number of failed blob retrievals from Celestia",
		}),
	}
}

// RecordSubmission records metrics for a successful submission
func (m *CelestiaMetrics) RecordSubmission(duration time.Duration, sizeBytes int) {
	m.SubmissionDuration.Observe(duration.Seconds())
	m.SubmissionSize.Observe(float64(sizeBytes))
	m.SubmissionsTotal.Inc()
}

// RecordSubmissionError records a submission error
func (m *CelestiaMetrics) RecordSubmissionError() {
	m.SubmissionErrors.Inc()
}

// RecordRetrieval records metrics for a successful retrieval
func (m *CelestiaMetrics) RecordRetrieval(duration time.Duration, sizeBytes int) {
	m.RetrievalDuration.Observe(duration.Seconds())
	m.RetrievalSize.Observe(float64(sizeBytes))
	m.RetrievalsTotal.Inc()
}

// RecordRetrievalError records a retrieval error
func (m *CelestiaMetrics) RecordRetrievalError() {
	m.RetrievalErrors.Inc()
}

// RecordHTTPRequest records an HTTP request duration (for GET/PUT endpoints)
// Deprecated: Use RecordHTTPRequestWithStatus instead
func (m *CelestiaMetrics) RecordHTTPRequest(method string, duration time.Duration) {
	m.RequestDuration.WithLabelValues(method).Observe(duration.Seconds())
}

// RecordHTTPRequestWithStatus records an HTTP request with status code for filtering
func (m *CelestiaMetrics) RecordHTTPRequestWithStatus(method string, statusCode int, duration time.Duration) {
	// For now, just record as before (will be enhanced in next step)
	m.RequestDuration.WithLabelValues(method).Observe(duration.Seconds())
}

// RecordBlobSize records the size of a blob
func (m *CelestiaMetrics) RecordBlobSize(sizeBytes int) {
	m.BlobSize.Observe(float64(sizeBytes))
}

// SetInclusionHeight sets the current inclusion height
func (m *CelestiaMetrics) SetInclusionHeight(height uint64) {
	m.InclusionHeight.Set(float64(height))
}

// RecordGetRequest records a GET request with status code (Option B)
func (m *CelestiaMetrics) RecordGetRequest(statusCode int, duration time.Duration) {
	status := statusCodeToString(statusCode)
	m.GetRequestDuration.WithLabelValues(status).Observe(duration.Seconds())
	m.GetRequestsTotal.WithLabelValues(status).Inc()
}

// RecordTimeToAvailability records the time from first GET to 200 OK (Option A)
func (m *CelestiaMetrics) RecordTimeToAvailability(duration time.Duration) {
	m.TimeToAvailability.Observe(duration.Seconds())
}

// RecordTimeToBatch records the time from PUT to batching
func (m *CelestiaMetrics) RecordTimeToBatch(duration time.Duration) {
	m.TimeToBatch.Observe(duration.Seconds())
}

// RecordTimeToConfirmation records the time from PUT to DA confirmation
func (m *CelestiaMetrics) RecordTimeToConfirmation(duration time.Duration) {
	m.TimeToConfirmation.Observe(duration.Seconds())
}

// statusCodeToString converts HTTP status code to string for labels
func statusCodeToString(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500:
		return "5xx"
	default:
		return "unknown"
	}
}
