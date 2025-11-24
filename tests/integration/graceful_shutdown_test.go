package integration

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	libshare "github.com/celestiaorg/go-square/v3/share"
	celestia "github.com/celestiaorg/op-alt-da"
	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/worker"
	"github.com/ethereum/go-ethereum/log"
)

// TestServer_GracefulShutdown tests server shutdown with active requests
func TestServer_GracefulShutdown(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)
	server := celestia.NewCelestiaServer(
		"localhost",
		0, // Random port
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: false},
		false,
		0,
		log.New(),
	)

	// Start server in background
	ctx, cancel := context.WithCancel(context.Background())
	var serverErr error
	serverDone := make(chan struct{})

	go func() {
		serverErr = server.Start(ctx)
		close(serverDone)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Trigger shutdown
	cancel()

	// Wait for server to stop (with timeout)
	select {
	case <-serverDone:
		// Server stopped successfully
		assert.True(t, serverErr == nil || serverErr == context.Canceled,
			"server should stop gracefully or with context.Canceled error")
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop within timeout")
	}
}

// TestServer_ShutdownWithInflightRequests tests shutdown while handling requests
func TestServer_ShutdownWithInflightRequests(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)
	server := celestia.NewCelestiaServer(
		"localhost",
		8765, // Fixed port for testing
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: false},
		false,
		0,
		log.New(),
	)

	// Start server
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	// Wait for server to be ready
	time.Sleep(200 * time.Millisecond)

	// Launch concurrent requests
	numRequests := 5
	var wg sync.WaitGroup
	requestResults := make([]bool, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			// Try to make request
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get("http://localhost:8765/health")
			if err == nil {
				resp.Body.Close()
				requestResults[index] = (resp.StatusCode == http.StatusOK)
			}
		}(i)
	}

	// Give requests a moment to start
	time.Sleep(50 * time.Millisecond)

	// Trigger shutdown
	cancel()

	// Wait for all requests to complete
	wg.Wait()

	// Wait for server to shutdown
	select {
	case <-serverDone:
		// Server stopped
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shutdown within timeout")
	}

	// At least some requests should have succeeded
	successCount := 0
	for _, success := range requestResults {
		if success {
			successCount++
		}
	}
	t.Logf("%d/%d requests succeeded during shutdown", successCount, numRequests)
}

// TestServer_ShutdownTimeout tests that shutdown completes within reasonable time
func TestServer_ShutdownTimeout(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)
	server := celestia.NewCelestiaServer(
		"localhost",
		0,
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: false},
		false,
		0,
		log.New(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Trigger shutdown and measure time
	start := time.Now()
	cancel()

	select {
	case <-serverDone:
		shutdownDuration := time.Since(start)
		t.Logf("Shutdown took %v", shutdownDuration)

		// Should complete within reasonable timeout (server uses 5s timeout)
		assert.Less(t, shutdownDuration, 10*time.Second,
			"shutdown should complete within 10 seconds")
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown did not complete within 15 seconds")
	}
}

// TestServer_MultipleShutdowns tests that multiple shutdown calls are safe
func TestServer_MultipleShutdowns(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)
	server := celestia.NewCelestiaServer(
		"localhost",
		0,
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: false},
		false,
		0,
		log.New(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	time.Sleep(100 * time.Millisecond)

	// Call cancel multiple times
	cancel()
	cancel() // Second call should be safe
	cancel() // Third call should be safe

	select {
	case <-serverDone:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}

	// Additional Stop() calls should be safe
	err := server.Stop()
	// Error is acceptable since server already stopped, but shouldn't panic
	_ = err
}

// TestServer_WorkersShutdownCleanly tests that workers stop cleanly
func TestServer_WorkersShutdownCleanly(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)

	// Enable all workers
	workerCfg := &worker.Config{
		ReadOnly:        false,
		SubmitPeriod:    1 * time.Second,
		ReconcilePeriod: 1 * time.Second,
		BackfillEnabled: true,
		BackfillPeriod:  1 * time.Second,
		SubmitTimeout:   5 * time.Second,
		MaxRetries:      3,
		MaxBlobWaitTime: 5 * time.Second,
		ReconcileAge:    10 * time.Second,
		GetTimeout:      5 * time.Second,
		StartHeight:     1,
		TrustedSigners:  []string{},
	}

	server := celestia.NewCelestiaServer(
		"localhost",
		0,
		store,
		celestiaStore,
		batch.DefaultConfig(),
		workerCfg,
		false,
		0,
		log.New(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	// Let workers run for a moment
	time.Sleep(300 * time.Millisecond)

	// Trigger shutdown
	shutdownStart := time.Now()
	cancel()

	// Wait for clean shutdown
	select {
	case <-serverDone:
		shutdownDuration := time.Since(shutdownStart)
		t.Logf("Workers shut down in %v", shutdownDuration)
		assert.Less(t, shutdownDuration, 10*time.Second,
			"workers should shutdown cleanly within timeout")
	case <-time.After(15 * time.Second):
		t.Fatal("workers did not shutdown within timeout")
	}
}

// TestServer_ReadOnlyMode tests shutdown in read-only mode (no submission worker)
func TestServer_ReadOnlyModeShutdown(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)

	server := celestia.NewCelestiaServer(
		"localhost",
		0,
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: true}, // Read-only mode
		false,
		0,
		log.New(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	time.Sleep(100 * time.Millisecond)

	cancel()

	select {
	case <-serverDone:
		// Success - read-only mode should shut down cleanly
	case <-time.After(10 * time.Second):
		t.Fatal("read-only server did not shutdown")
	}
}

// TestServer_ShutdownWithMetrics tests shutdown with metrics enabled
func TestServer_ShutdownWithMetrics(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)

	server := celestia.NewCelestiaServer(
		"localhost",
		0,
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: false},
		true,  // Metrics enabled
		19090, // Metrics port
		log.New(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	// Wait for servers to start
	time.Sleep(200 * time.Millisecond)

	// Verify metrics endpoint is up
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get("http://localhost:19090/metrics")
	if err == nil {
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "metrics should be accessible")
	}

	// Shutdown
	cancel()

	select {
	case <-serverDone:
		// Both servers should shutdown cleanly
	case <-time.After(10 * time.Second):
		t.Fatal("server with metrics did not shutdown")
	}
}

// TestServer_RepeatedStartStop tests starting and stopping server multiple times
func TestServer_RepeatedStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping repeated start/stop test in short mode")
	}

	store, celestiaStore := setupTestShutdownServer(t)

	for i := 0; i < 3; i++ {
		t.Logf("Iteration %d", i+1)

		server := celestia.NewCelestiaServer(
			"localhost",
			0,
			store,
			celestiaStore,
			batch.DefaultConfig(),
			&worker.Config{ReadOnly: false},
			false,
			0,
			log.New(),
		)

		ctx, cancel := context.WithCancel(context.Background())
		serverDone := make(chan struct{})

		go func() {
			server.Start(ctx)
			close(serverDone)
		}()

		time.Sleep(100 * time.Millisecond)

		cancel()

		select {
		case <-serverDone:
			// Success
		case <-time.After(10 * time.Second):
			t.Fatalf("iteration %d: server did not stop", i+1)
		}
	}
}

// TestServer_ShutdownDuringDatabaseOperation tests shutdown during DB writes
func TestServer_ShutdownDuringDatabaseOperation(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)

	server := celestia.NewCelestiaServer(
		"localhost",
		8766,
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: false},
		false,
		0,
		log.New(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	time.Sleep(100 * time.Millisecond)

	// Start some database operations
	dbOperations := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			blob := &db.Blob{
				Commitment: []byte("test_commit_" + string(rune(i))),
				Data:       []byte("test_data"),
				Size:       9,
				Status:     "pending",
				Namespace:  celestiaStore.Namespace.Bytes(),
			}
			store.InsertBlob(context.Background(), blob)
			time.Sleep(10 * time.Millisecond)
		}
		close(dbOperations)
	}()

	// Shutdown during operations
	time.Sleep(30 * time.Millisecond)
	cancel()

	// Wait for both to complete
	<-dbOperations
	<-serverDone

	// Database should still be in consistent state
	stats, err := store.GetStats(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, stats)
}

// TestServer_ImmediateShutdown tests shutdown immediately after start
func TestServer_ImmediateShutdown(t *testing.T) {
	store, celestiaStore := setupTestShutdownServer(t)

	server := celestia.NewCelestiaServer(
		"localhost",
		0,
		store,
		celestiaStore,
		batch.DefaultConfig(),
		&worker.Config{ReadOnly: false},
		false,
		0,
		log.New(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		server.Start(ctx)
		close(serverDone)
	}()

	// Cancel immediately
	cancel()

	// Should still shutdown cleanly
	select {
	case <-serverDone:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("immediate shutdown failed")
	}
}

// Helper function
func setupTestShutdownServer(t *testing.T) (*db.BlobStore, *celestia.CelestiaStore) {
	store, err := db.NewBlobStore(":memory:")
	require.NoError(t, err)

	nsBytes := []byte{
		0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}

	celestiaStore := &celestia.CelestiaStore{
		Log: log.New(),
	}

	ns, err := libshare.NewNamespaceFromBytes(nsBytes)
	require.NoError(t, err)
	celestiaStore.Namespace = ns

	return store, celestiaStore
}
