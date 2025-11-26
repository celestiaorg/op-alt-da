package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	writerURL   = "http://localhost:3100"
	readerURL   = "http://localhost:3101"
	blobSize    = 900 * 1024 // 100 KB
	putInterval = 1 * time.Second
	readerLag   = 35 * time.Second // 30-40 seconds
	getInterval = 20 * time.Second
)

type blobData struct {
	commitment   []byte
	originalData []byte
	timestamp    time.Time
	blobNum      int
}

type stats struct {
	mu               sync.Mutex
	putsTotal        int
	putsFailed       int
	getsTotal        int
	getsSuccess      int
	getsFailed       int
	getsNotFound     int
	getsDataMismatch int
}

func (s *stats) recordPut(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putsTotal++
	if !success {
		s.putsFailed++
	}
}

func (s *stats) recordGet(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getsTotal++
	switch status {
	case "success":
		s.getsSuccess++
	case "not_found":
		s.getsNotFound++
	case "failed":
		s.getsFailed++
	case "data_mismatch":
		s.getsDataMismatch++
	}
}

func (s *stats) print() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Println("\n========================================")
	fmt.Println("FINAL TEST STATISTICS")
	fmt.Println("========================================")
	fmt.Printf("PUT Requests:\n")
	fmt.Printf("  Total:   %d\n", s.putsTotal)
	fmt.Printf("  Failed:  %d\n", s.putsFailed)
	fmt.Printf("  Success: %d\n", s.putsTotal-s.putsFailed)
	fmt.Printf("\nGET Requests:\n")
	fmt.Printf("  Total:          %d\n", s.getsTotal)
	fmt.Printf("  Success:        %d\n", s.getsSuccess)
	fmt.Printf("  Not Found:      %d (pending confirmation)\n", s.getsNotFound)
	fmt.Printf("  Data Mismatch:  %d ⚠️\n", s.getsDataMismatch)
	fmt.Printf("  Failed:         %d\n", s.getsFailed)

	if s.getsDataMismatch > 0 {
		fmt.Printf("\n⚠️  WARNING: Data mismatch detected - blob data corrupted!\n")
	} else if s.getsSuccess > 0 {
		fmt.Printf("\n✓ All retrieved blobs match original data (integrity verified)\n")
	}
	fmt.Println("========================================")
}

func main() {
	// Parse command-line flags
	durationSecs := flag.Int("duration", 100, "Test duration in seconds (e.g., 100, 600)")
	flag.Parse()

	testDuration := time.Duration(*durationSecs) * time.Second

	fmt.Println("========================================")
	fmt.Println("Writer/Reader Integration Test")
	fmt.Println("========================================")
	fmt.Printf("Writer endpoint: %s\n", writerURL)
	fmt.Printf("Reader endpoint: %s\n", readerURL)
	fmt.Printf("Blob size: %d bytes (100 KB)\n", blobSize)
	fmt.Printf("PUT interval: %v\n", putInterval)
	fmt.Printf("Reader lag: %v\n", readerLag)
	fmt.Printf("GET interval: %v\n", getInterval)
	fmt.Printf("Test duration: %v (%d seconds)\n", testDuration, *durationSecs)
	fmt.Printf("Expected blobs: ~%d\n", *durationSecs)
	fmt.Println("========================================")

	// Shared state
	var blobs []blobData
	var blobsMu sync.Mutex
	var statsData stats
	var wg sync.WaitGroup

	startTime := time.Now()

	// Writer goroutine: PUT blobs to writer server
	wg.Add(1)
	go func() {
		defer wg.Done()
		fmt.Println("[WRITER] Starting writer goroutine...")

		writerDeadline := startTime.Add(testDuration)
		blobNum := 0
		lastReportTime := time.Now()
		batchCount := 0

		for time.Now().Before(writerDeadline) {
			blobNum++
			batchCount++

			// Generate random data
			data := make([]byte, blobSize)
			if _, err := rand.Read(data); err != nil {
				fmt.Printf("[WRITER] ERROR: Failed to generate random data for blob %d: %v\n", blobNum, err)
				statsData.recordPut(false)
				time.Sleep(putInterval)
				continue
			}

			// PUT request
			resp, err := http.Post(writerURL+"/put", "application/octet-stream", bytes.NewReader(data))
			if err != nil {
				fmt.Printf("[WRITER] ERROR: PUT request failed for blob %d: %v\n", blobNum, err)
				statsData.recordPut(false)
				time.Sleep(putInterval)
				continue
			}

			// Read commitment
			commitment, err := io.ReadAll(resp.Body)
			resp.Body.Close()

			if err != nil {
				fmt.Printf("[WRITER] ERROR: Failed to read commitment for blob %d: %v\n", blobNum, err)
				statsData.recordPut(false)
				time.Sleep(putInterval)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				fmt.Printf("[WRITER] ERROR: PUT returned status %d for blob %d\n", resp.StatusCode, blobNum)
				statsData.recordPut(false)
				time.Sleep(putInterval)
				continue
			}

			// Store blob data with original data for verification
			blobsMu.Lock()
			blobs = append(blobs, blobData{
				commitment:   commitment,
				originalData: data,
				timestamp:    time.Now(),
				blobNum:      blobNum,
			})
			blobsMu.Unlock()

			statsData.recordPut(true)

			// Batch log every 10 blobs or 10 seconds
			if batchCount >= 10 || time.Since(lastReportTime) >= 10*time.Second {
				fmt.Printf("[WRITER] Progress: %d blobs written (latest: #%d, commitment: %x...)\n",
					blobNum, blobNum, commitment[:8])
				lastReportTime = time.Now()
				batchCount = 0
			}

			// Wait before next PUT
			time.Sleep(putInterval)
		}

		fmt.Printf("[WRITER] Finished sending %d blobs\n", blobNum)
	}()

	// Reader goroutine: GET blobs from reader server after lag
	// Sequential checking like op-node: check oldest unconfirmed first
	wg.Add(1)
	go func() {
		defer wg.Done()
		fmt.Printf("[READER] Waiting %v before starting GET requests...\n", readerLag)
		time.Sleep(readerLag)
		fmt.Println("[READER] Starting reader goroutine...")

		ticker := time.NewTicker(getInterval)
		defer ticker.Stop()

		// Track which blobs have been confirmed (op-node behavior)
		lastConfirmedIndex := -1

		// Reader runs until: test duration + reader lag + extra time for final checks
		readerDeadline := startTime.Add(testDuration + readerLag + 3*getInterval)

		for {
			<-ticker.C

			// Check if we should stop
			if time.Now().After(readerDeadline) {
				fmt.Println("[READER] Test duration complete, stopping reader")
				break
			}

			blobsMu.Lock()
			totalBlobs := len(blobs)
			blobsToCheck := make([]blobData, len(blobs))
			copy(blobsToCheck, blobs)
			blobsMu.Unlock()

			if totalBlobs == 0 {
				fmt.Println("[READER] No blobs available yet, waiting...")
				continue
			}

			// Sequential checking: start from first unconfirmed blob (op-node pattern)
			// Check up to 10 blobs per round
			startIdx := lastConfirmedIndex + 1
			endIdx := startIdx + 10
			if endIdx > totalBlobs {
				endIdx = totalBlobs
			}

			if startIdx >= totalBlobs {
				fmt.Printf("[READER] All %d blobs confirmed! Waiting for new blobs...\n", totalBlobs)
				continue
			}

			fmt.Printf("\n[READER] ===== GET Check Round (checking blobs %d-%d of %d) =====\n",
				startIdx+1, endIdx, totalBlobs)

			successCount := 0
			notFoundCount := 0
			mismatchCount := 0
			errorCount := 0

			// Check sequentially and stop at first failure (op-node behavior)
			for i := startIdx; i < endIdx; i++ {
				b := blobsToCheck[i]
				commitmentHex := hex.EncodeToString(b.commitment)
				url := fmt.Sprintf("%s/get/0x%s", readerURL, commitmentHex)

				resp, err := http.Get(url)
				if err != nil {
					fmt.Printf("[READER] ✗ Blob #%d: GET request failed: %v\n", b.blobNum, err)
					statsData.recordGet("failed")
					errorCount++
					// Stop on error - retry next round
					break
				}

				retrievedData, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				age := time.Since(b.timestamp)

				switch resp.StatusCode {
				case http.StatusOK:
					// Verify data integrity
					if !bytes.Equal(retrievedData, b.originalData) {
						fmt.Printf("[READER] ⚠️  Blob #%d: DATA MISMATCH! (age: %v, size: got %d, expected %d)\n",
							b.blobNum, age.Round(time.Second), len(retrievedData), len(b.originalData))
						statsData.recordGet("data_mismatch")
						mismatchCount++
						// Stop on mismatch - something is wrong
						break
					} else {
						fmt.Printf("[READER] ✓ Blob #%d: OK (age: %v, size: %d bytes, data verified)\n",
							b.blobNum, age.Round(time.Second), len(retrievedData))
						statsData.recordGet("success")
						successCount++
						// Mark this blob as confirmed and advance
						lastConfirmedIndex = i
					}
				case http.StatusNotFound:
					fmt.Printf("[READER] ⏳ Blob #%d: Not found (age: %v) - pending backfill, will retry\n",
						b.blobNum, age.Round(time.Second))
					statsData.recordGet("not_found")
					notFoundCount++
					// Stop here - wait for this blob before checking later ones
					break
				default:
					fmt.Printf("[READER] ✗ Blob #%d: Unexpected status %d (age: %v)\n",
						b.blobNum, resp.StatusCode, age.Round(time.Second))
					statsData.recordGet("failed")
					errorCount++
					// Stop on unexpected error
					break
				}
			}

			fmt.Printf("[READER] Round summary: ✓ %d success, ⏳ %d pending, ⚠️  %d mismatch, ✗ %d failed (last confirmed: blob #%d)\n",
				successCount, notFoundCount, mismatchCount, errorCount, lastConfirmedIndex+1)
		}

		fmt.Println("[READER] Finished checking blobs")
	}()

	// Wait for both goroutines
	wg.Wait()

	// Print final statistics
	statsData.print()

	fmt.Printf("\nTest duration: %v\n", time.Since(startTime).Round(time.Second))
	fmt.Println("\nTest completed!")
}
