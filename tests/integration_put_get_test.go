//go:build integration
// +build integration

package tests

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	celestia "github.com/celestiaorg/op-alt-da"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
)

// pickFreePort finds an available TCP port on localhost.
func pickFreePort(t *testing.T) (int, func()) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	port := addr.Port
	return port, func() { _ = l.Close() }
}

func TestIntegration_PutThenGet_RoundTrip(t *testing.T) {
	t.Parallel()

	// Require user-provided devnet configuration
	bridge := os.Getenv("CELESTIA_BRIDGE")
	auth := os.Getenv("CELESTIA_AUTH_TOKEN")
	ns := os.Getenv("CELESTIA_NAMESPACE")
	if bridge == "" || auth == "" || ns == "" {
		t.Skip("CELESTIA_BRIDGE, CELESTIA_AUTH_TOKEN, and CELESTIA_NAMESPACE must be set to run this integration test")
	}

	// Build the server binary from the repo root (parent of ./tests)
	cwd, _ := os.Getwd()
	repoRoot := filepath.Dir(cwd)
	binPath := filepath.Join(repoRoot, "bin", "da-server")
	if _, err := os.Stat(binPath); err != nil {
		build := exec.Command("go", "build", "-o", binPath, "./cmd/daserver")
		build.Dir = repoRoot
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			t.Fatalf("failed to build da-server: %v", err)
		}
	}

	// Reserve a port, then start server on it
	port, release := pickFreePort(t)
	release()

	// Start da-server with env mapped from CELESTIA_* → OP_ALTDA_*
	addr := "127.0.0.1"
	serverURL := fmt.Sprintf("http://%s:%d", addr, port)

	cmd := exec.Command(binPath,
		"--addr", addr,
		"--port", fmt.Sprintf("%d", port),
	)
	cmd.Env = append(os.Environ(),
		"OP_ALTDA_CELESTIA_SERVER="+bridge,
		"OP_ALTDA_CELESTIA_AUTH_TOKEN="+auth,
		"OP_ALTDA_CELESTIA_NAMESPACE="+ns,
		"OP_ALTDA_LOG_LEVEL=INFO", // ensure we see submission logs
	)

	// Capture logs to extract blob id later
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start da-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Wait for server to accept connections (TCP dial loop)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", addr, port), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	// small additional delay to let routes be ready
	time.Sleep(500 * time.Millisecond)

	// Generate test payload with meaningful data
	rnd := rand.New(rand.NewSource(42))

	// Create a structured payload with metadata and random data
	metadata := fmt.Sprintf("test-id:%d, timestamp:%d, test:integration-put-get",
		time.Now().Unix(), time.Now().UnixNano())
	randomData := make([]byte, 200)
	_, _ = rnd.Read(randomData)

	// Combine metadata and random data
	payload := []byte(fmt.Sprintf("OP-ALT-DA Integration Test\n%s\n", metadata))
	payload = append(payload, randomData...)

	// Ensure minimum size for testing
	if len(payload) < 256 {
		additional := make([]byte, 256-len(payload))
		_, _ = rnd.Read(additional)
		payload = append(payload, additional...)
	}

	// PUT the payload
	putReq, _ := http.NewRequest(http.MethodPut, serverURL+"/put", bytes.NewReader(payload))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT request failed: %v\nstdout:%s\nstderr:%s", err, outBuf.String(), errBuf.String())
	}
	io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		t.Fatalf("PUT unexpected status %d\nstdout:%s\nstderr:%s", putResp.StatusCode, outBuf.String(), errBuf.String())
	}

	// Extract blob `id` (hex) from server logs.
	// celestia_storage.go logs: "celestia: blob successfully submitted", key-value with id=<hex>
	var blobIDHex string
	idRe := regexp.MustCompile(`blob successfully submitted"\s*id=([0-9a-fA-F]+)`) // tolerant of logfmt/jsonish

	// Wait up to 60s for the log line to appear
	waitLogsDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(waitLogsDeadline) {
		logs := outBuf.String() + "\n" + errBuf.String()
		if m := idRe.FindStringSubmatch(logs); len(m) == 2 {
			blobIDHex = m[1]
			break
		}
		// permissive scan
		for _, ln := range strings.Split(logs, "\n") {
			if strings.Contains(ln, "blob successfully submitted") && strings.Contains(ln, "id") {
				parts := strings.FieldsFunc(ln, func(r rune) bool { return r == ' ' || r == '=' || r == '"' || r == ',' || r == '\t' })
				for i := 0; i < len(parts)-1; i++ {
					if parts[i] == "id" && parts[i+1] != "" {
						blobIDHex = parts[i+1]
						break
					}
				}
			}
			if blobIDHex != "" {
				break
			}
		}
		if blobIDHex != "" {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if blobIDHex == "" {
		t.Fatalf("could not find blob id in logs within 60s\nstdout:\n%s\n--- stderr ---\n%s", outBuf.String(), errBuf.String())
	}

	// Reconstruct the commitment key exactly as server does:
	// commitment := altda.NewGenericCommitment(append([]byte{celestia.VersionByte}, id...)).Encode()
	idBytes, err := hex.DecodeString(blobIDHex)
	if err != nil {
		t.Fatalf("invalid hex id: %v", err)
	}
	commitment := altda.NewGenericCommitment(append([]byte{celestia.VersionByte}, idBytes...)).Encode()
	hexKey := hex.EncodeToString(commitment)

	// Poll GET until success or global timeout (a few minutes, as requested)
	// Allow overriding via OP_ALTDA_TEST_GET_TIMEOUT_MIN (minutes)
	getTimeout := 8 * time.Minute
	if minStr := os.Getenv("OP_ALTDA_TEST_GET_TIMEOUT_MIN"); minStr != "" {
		if mins, err := strconv.Atoi(minStr); err == nil && mins > 0 {
			getTimeout = time.Duration(mins) * time.Minute
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), getTimeout)
	defer cancel()

	var got []byte
	sleep := 2 * time.Second
	maxSleep := 15 * time.Second
	var lastStatus int
	var lastBodySnippet string
	for {
		if ctx.Err() != nil {
			break
		}
		// Server expects 0x-prefixed hex for hexutil.Decode
		u := serverURL + "/get/0x" + hexKey
		resp, err := http.Get(u)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				got = b
				break
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastStatus = resp.StatusCode
			if len(b) > 256 {
				b = b[:256]
			}
			lastBodySnippet = string(b)
		}
		time.Sleep(sleep)
		// exponential backoff with cap
		sleep *= 2
		if sleep > maxSleep {
			sleep = maxSleep
		}
	}

	if ctx.Err() != nil {
		t.Fatalf("timed out waiting for GET to return data for key %s (lastStatus=%d, lastBodySnippet=%q)\n--- stdout ---\n%s\n--- stderr ---\n%s", hexKey, lastStatus, lastBodySnippet, outBuf.String(), errBuf.String())
	}

	if !bytes.Equal(got, payload) {
		t.Fatalf("data mismatch: want %d bytes, got %d", len(payload), len(got))
	}
}
