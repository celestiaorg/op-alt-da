package tests

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
)

// setupBenchmarkHandler creates a mock HTTP handler that simulates the PUT endpoint.
// This benchmarks the HTTP request/response overhead including:
// - Request body reading
// - Response writing
// - Basic processing overhead
//
// Note: This does NOT include actual Celestia network submission time.
// For end-to-end benchmarks including Celestia network calls, use integration tests.
func setupBenchmarkHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Simulate reading request body (this is what the real handler does)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Simulate minimal processing (commitment generation)
		// In real implementation, this would call store.Put()
		commitment := make([]byte, 32)
		rand.New(rand.NewSource(int64(len(body)))).Read(commitment)

		// Write response (what the real handler does)
		w.WriteHeader(http.StatusOK)
		w.Write(commitment)
	}
}

// BenchmarkPut measures the performance of the PUT endpoint with different payload sizes
func BenchmarkPut(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1KB", 1024},
		{"10KB", 10 * 1024},
		{"100KB", 100 * 1024},
		{"1MB", 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
	}

	handler := setupBenchmarkHandler()

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			// Generate test data
			data := make([]byte, size.size)
			rand.New(rand.NewSource(42)).Read(data)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				req := httptest.NewRequest(http.MethodPut, "/put", bytes.NewReader(data))
				w := httptest.NewRecorder()

				handler(w, req)

				if w.Code != http.StatusOK {
					b.Fatalf("unexpected status code: %d", w.Code)
				}
			}

			b.SetBytes(int64(size.size))
		})
	}
}

// BenchmarkPutConcurrent measures PUT performance under concurrent load
func BenchmarkPutConcurrent(b *testing.B) {
	handler := setupBenchmarkHandler()

	dataSize := 10 * 1024 // 10KB
	data := make([]byte, dataSize)
	rand.New(rand.NewSource(42)).Read(data)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodPut, "/put", bytes.NewReader(data))
			w := httptest.NewRecorder()

			handler(w, req)

			if w.Code != http.StatusOK {
				b.Fatalf("unexpected status code: %d", w.Code)
			}
		}
	})

	b.SetBytes(int64(dataSize))
}

// BenchmarkPutThroughput measures requests per second
func BenchmarkPutThroughput(b *testing.B) {
	handler := setupBenchmarkHandler()

	dataSize := 1024 // 1KB
	data := make([]byte, dataSize)
	rand.New(rand.NewSource(42)).Read(data)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPut, "/put", bytes.NewReader(data))
		w := httptest.NewRecorder()

		handler(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("unexpected status code: %d", w.Code)
		}
	}

	b.SetBytes(int64(dataSize))
}
