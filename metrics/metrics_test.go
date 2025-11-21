package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestCelestiaMetrics_HTTPMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewCelestiaMetrics(registry)

	// Test HTTP request duration metric
	metrics.RecordHTTPRequest("put", 100*time.Millisecond)
	metrics.RecordHTTPRequest("get", 50*time.Millisecond)

	// Test blob size metric
	metrics.RecordBlobSize(1024)
	metrics.RecordBlobSize(2048)

	// Test inclusion height metric
	metrics.SetInclusionHeight(12345)

	// Verify metrics can be collected
	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	// Check that we have the expected metrics
	metricNames := make(map[string]bool)
	for _, mf := range metricFamilies {
		metricNames[mf.GetName()] = true
	}

	// Verify HTTP-level metrics from main branch are present
	expectedMetrics := []string{
		"op_altda_request_duration_seconds",
		"op_altda_blob_size_bytes",
		"op_altda_inclusion_height",
	}

	for _, name := range expectedMetrics {
		if !metricNames[name] {
			t.Errorf("Expected metric %s not found", name)
		}
	}

	// Verify request duration has both "get" and "put" labels
	count := testutil.CollectAndCount(metrics.RequestDuration)
	if count != 2 {
		t.Errorf("Expected 2 request duration metrics (get and put), got %d", count)
	}

	// Verify blob size histogram has observations
	blobSizeCount := testutil.CollectAndCount(metrics.BlobSize)
	if blobSizeCount == 0 {
		t.Error("Expected blob size metric to have observations")
	}

	// Verify inclusion height gauge is set
	gaugeValue := testutil.ToFloat64(metrics.InclusionHeight)
	if gaugeValue != 12345 {
		t.Errorf("Expected inclusion height 12345, got %f", gaugeValue)
	}
}

func TestCelestiaMetrics_WorkerMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewCelestiaMetrics(registry)

	// Test submission metrics
	metrics.RecordSubmission(200*time.Millisecond, 1024*1024)
	metrics.RecordSubmissionError()

	// Test retrieval metrics
	metrics.RecordRetrieval(50*time.Millisecond, 512*1024)
	metrics.RecordRetrievalError()

	// Verify metrics can be collected
	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	// Check that we have the expected worker-level metrics
	metricNames := make(map[string]bool)
	for _, mf := range metricFamilies {
		metricNames[mf.GetName()] = true
	}

	// Verify worker-level metrics are present
	expectedMetrics := []string{
		"celestia_submission_duration_seconds",
		"celestia_submission_size_bytes",
		"celestia_submissions_total",
		"celestia_submission_errors_total",
		"celestia_retrieval_duration_seconds",
		"celestia_retrieval_size_bytes",
		"celestia_retrievals_total",
		"celestia_retrieval_errors_total",
	}

	for _, name := range expectedMetrics {
		if !metricNames[name] {
			t.Errorf("Expected metric %s not found", name)
		}
	}

	// Verify counter values
	submissionsTotal := testutil.ToFloat64(metrics.SubmissionsTotal)
	if submissionsTotal != 1 {
		t.Errorf("Expected 1 submission, got %f", submissionsTotal)
	}

	submissionErrors := testutil.ToFloat64(metrics.SubmissionErrors)
	if submissionErrors != 1 {
		t.Errorf("Expected 1 submission error, got %f", submissionErrors)
	}

	retrievalsTotal := testutil.ToFloat64(metrics.RetrievalsTotal)
	if retrievalsTotal != 1 {
		t.Errorf("Expected 1 retrieval, got %f", retrievalsTotal)
	}

	retrievalErrors := testutil.ToFloat64(metrics.RetrievalErrors)
	if retrievalErrors != 1 {
		t.Errorf("Expected 1 retrieval error, got %f", retrievalErrors)
	}
}

func TestCelestiaMetrics_MetricNamingConvention(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewCelestiaMetrics(registry)

	// Record some sample data
	metrics.RecordHTTPRequest("put", 100*time.Millisecond)
	metrics.RecordSubmission(200*time.Millisecond, 1024)

	// Gather metrics
	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	// Verify naming conventions:
	// - HTTP-level metrics from main branch use "op_altda_" prefix
	// - Worker-level metrics use "celestia_" prefix
	for _, mf := range metricFamilies {
		name := mf.GetName()

		// Check that metrics follow expected naming patterns
		if strings.HasPrefix(name, "op_altda_") {
			// HTTP-level metrics (from main branch)
			validNames := []string{
				"op_altda_request_duration_seconds",
				"op_altda_blob_size_bytes",
				"op_altda_inclusion_height",
			}
			found := false
			for _, valid := range validNames {
				if name == valid {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Unexpected op_altda_ metric: %s", name)
			}
		} else if strings.HasPrefix(name, "celestia_") {
			// Worker-level metrics - all valid names
			validNames := []string{
				"celestia_submission_duration_seconds",
				"celestia_submission_size_bytes",
				"celestia_submissions_total",
				"celestia_submission_errors_total",
				"celestia_retrieval_duration_seconds",
				"celestia_retrieval_size_bytes",
				"celestia_retrievals_total",
				"celestia_retrieval_errors_total",
			}
			found := false
			for _, valid := range validNames {
				if name == valid {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Unexpected celestia_ metric: %s", name)
			}
		} else {
			t.Errorf("Metric with unexpected prefix: %s", name)
		}
	}
}

func TestCelestiaMetrics_NilRegistry(t *testing.T) {
	// Test that passing nil registry doesn't panic
	metrics := NewCelestiaMetrics(nil)

	// Should be able to call all methods without panicking
	metrics.RecordHTTPRequest("put", 100*time.Millisecond)
	metrics.RecordBlobSize(1024)
	metrics.SetInclusionHeight(12345)
	metrics.RecordSubmission(200*time.Millisecond, 1024)
	metrics.RecordSubmissionError()
	metrics.RecordRetrieval(50*time.Millisecond, 512)
	metrics.RecordRetrievalError()
}
