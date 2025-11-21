package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CelestiaMetrics tracks performance metrics for Celestia DA operations
type CelestiaMetrics struct {
	// Submission metrics (from worker perspective)
	SubmissionDuration prometheus.Histogram
	SubmissionSize     prometheus.Histogram
	SubmissionsTotal   prometheus.Counter
	SubmissionErrors   prometheus.Counter

	// Retrieval metrics (from worker/handler perspective)
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
		// Submission metrics
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
