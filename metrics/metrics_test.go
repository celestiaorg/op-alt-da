package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCelestiaMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	require.NotNil(t, m)
	assert.NotNil(t, m.RequestDuration)
	assert.NotNil(t, m.BlobSize)
	assert.NotNil(t, m.InclusionHeight)
	assert.NotNil(t, m.SubmissionDuration)
	assert.NotNil(t, m.SubmissionsTotal)
	assert.NotNil(t, m.SubmissionErrors)
	assert.NotNil(t, m.RetrievalDuration)
	assert.NotNil(t, m.RetrievalsTotal)
	assert.NotNil(t, m.RetrievalErrors)
}

func TestRecordHTTPRequest(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	// Record GET and PUT requests
	m.RecordHTTPRequest("get", 100*time.Millisecond)
	m.RecordHTTPRequest("put", 500*time.Millisecond)
	m.RecordHTTPRequest("get", 200*time.Millisecond)

	// Verify metrics are registered
	metrics, err := registry.Gather()
	require.NoError(t, err)

	found := false
	for _, metric := range metrics {
		if metric.GetName() == "op_altda_request_duration_seconds" {
			found = true
			break
		}
	}
	assert.True(t, found, "request duration metric should be registered")
}

func TestRecordBlobSize(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	m.RecordBlobSize(1024)
	m.RecordBlobSize(65536)
	m.RecordBlobSize(1048576)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	found := false
	for _, metric := range metrics {
		if metric.GetName() == "op_altda_blob_size_bytes" {
			found = true
			break
		}
	}
	assert.True(t, found, "blob size metric should be registered")
}

func TestSetInclusionHeight(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	m.SetInclusionHeight(12345)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	found := false
	for _, metric := range metrics {
		if metric.GetName() == "op_altda_inclusion_height" {
			found = true
			// Verify the gauge value
			require.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(12345), metric.GetMetric()[0].GetGauge().GetValue())
			break
		}
	}
	assert.True(t, found, "inclusion height metric should be registered")
}

func TestRecordSubmission(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	m.RecordSubmission(5*time.Second, 4096)
	m.RecordSubmission(10*time.Second, 8192)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	foundDuration := false
	foundTotal := false
	for _, metric := range metrics {
		switch metric.GetName() {
		case "celestia_submission_duration_seconds":
			foundDuration = true
		case "celestia_submissions_total":
			foundTotal = true
			require.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(2), metric.GetMetric()[0].GetCounter().GetValue())
		}
	}
	assert.True(t, foundDuration, "submission duration metric should be registered")
	assert.True(t, foundTotal, "submissions total metric should be registered")
}

func TestRecordSubmissionError(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	m.RecordSubmissionError()
	m.RecordSubmissionError()
	m.RecordSubmissionError()

	metrics, err := registry.Gather()
	require.NoError(t, err)

	found := false
	for _, metric := range metrics {
		if metric.GetName() == "celestia_submission_errors_total" {
			found = true
			require.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(3), metric.GetMetric()[0].GetCounter().GetValue())
			break
		}
	}
	assert.True(t, found, "submission errors metric should be registered")
}

func TestRecordRetrieval(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	m.RecordRetrieval(100*time.Millisecond, 2048)
	m.RecordRetrieval(500*time.Millisecond, 4096)
	m.RecordRetrieval(1*time.Second, 8192)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	foundDuration := false
	foundTotal := false
	for _, metric := range metrics {
		switch metric.GetName() {
		case "celestia_retrieval_duration_seconds":
			foundDuration = true
		case "celestia_retrievals_total":
			foundTotal = true
			require.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(3), metric.GetMetric()[0].GetCounter().GetValue())
		}
	}
	assert.True(t, foundDuration, "retrieval duration metric should be registered")
	assert.True(t, foundTotal, "retrievals total metric should be registered")
}

func TestRecordRetrievalError(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	m.RecordRetrievalError()
	m.RecordRetrievalError()

	metrics, err := registry.Gather()
	require.NoError(t, err)

	found := false
	for _, metric := range metrics {
		if metric.GetName() == "celestia_retrieval_errors_total" {
			found = true
			require.Len(t, metric.GetMetric(), 1)
			assert.Equal(t, float64(2), metric.GetMetric()[0].GetCounter().GetValue())
			break
		}
	}
	assert.True(t, found, "retrieval errors metric should be registered")
}

// TestNoBatchMetrics ensures that batch-related metrics are NOT included
func TestNoBatchMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	// Trigger some metrics to populate the registry
	m.RecordSubmission(time.Second, 1024)
	m.RecordRetrieval(time.Second, 1024)

	metrics, err := registry.Gather()
	require.NoError(t, err)

	batchMetricNames := []string{
		"time_to_batch",
		"time_to_confirmation",
		"time_to_availability",
		"batch_size",
		"batch_duration",
	}

	for _, metric := range metrics {
		for _, batchName := range batchMetricNames {
			assert.NotContains(t, metric.GetName(), batchName,
				"should not contain batch-related metric: %s", metric.GetName())
		}
	}
}

// TestAllMetricsRegistered verifies all expected metrics are properly registered
func TestAllMetricsRegistered(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewCelestiaMetrics(registry)

	// Trigger all metrics
	m.RecordHTTPRequest("get", time.Second)
	m.RecordHTTPRequest("put", time.Second)
	m.RecordBlobSize(1024)
	m.SetInclusionHeight(100)
	m.RecordSubmission(time.Second, 1024)
	m.RecordSubmissionError()
	m.RecordRetrieval(time.Second, 1024)
	m.RecordRetrievalError()

	metrics, err := registry.Gather()
	require.NoError(t, err)

	expectedMetrics := map[string]bool{
		"op_altda_request_duration_seconds":   false,
		"op_altda_blob_size_bytes":            false,
		"op_altda_inclusion_height":           false,
		"celestia_submission_duration_seconds": false,
		"celestia_submissions_total":           false,
		"celestia_submission_errors_total":     false,
		"celestia_retrieval_duration_seconds":  false,
		"celestia_retrievals_total":            false,
		"celestia_retrieval_errors_total":      false,
	}

	for _, metric := range metrics {
		if _, exists := expectedMetrics[metric.GetName()]; exists {
			expectedMetrics[metric.GetName()] = true
		}
	}

	for name, found := range expectedMetrics {
		assert.True(t, found, "expected metric %q to be registered", name)
	}
}

