# DA Server API Reference

This document describes the HTTP API for the stateless Celestia DA server.

## Base URL

By default, the server listens on `http://127.0.0.1:3100`.

---

## Endpoints

### PUT /put

Submit a blob to Celestia and receive a commitment.

**Important**: This endpoint **blocks** until Celestia confirms the blob. Expected wait time is **10-60 seconds** depending on network conditions.

#### Request

- **Method**: `PUT` or `POST`
- **Path**: `/put` or `/put/`
- **Content-Type**: `application/octet-stream`
- **Body**: Raw blob data (bytes)

#### Response

- **Success (200 OK)**: Hex-encoded commitment
- **Error (400 Bad Request)**: Invalid request (e.g., empty blob)
- **Error (500 Internal Server Error)**: Submission failed (client should retry)

#### Commitment Format

The returned commitment follows the Generic Commitment format:

```
[commitment_type][version_byte][blob_id]
```

Where:
- `commitment_type`: `0x01` (Generic Commitment)
- `version_byte`: `0x0c` (Celestia version)
- `blob_id`: Encoded CelestiaBlobID (height + commitment, optionally with share offset/size)

#### Example

```bash
# Submit a blob (expect 10-60 second wait)
curl -X PUT http://localhost:3100/put \
  -H "Content-Type: application/octet-stream" \
  --data-binary "hello world"

# Response (hex-encoded commitment)
0x010c39300000000000001234567890abcdef...
```

#### Error Responses

| Status | Description | Action |
|--------|-------------|--------|
| 400 | Empty blob data | Fix request body |
| 400 | Invalid route | Check URL path |
| 500 | Submission failed | Retry with backoff |

---

### GET /get/:commitment

Retrieve a blob from Celestia using its commitment.

#### Request

- **Method**: `GET`
- **Path**: `/get/<hex-encoded-commitment>`
- **Parameters**: 
  - `commitment`: Hex-encoded commitment (with or without `0x` prefix)

#### Response

- **Success (200 OK)**: Raw blob data (bytes)
- **Error (400 Bad Request)**: Invalid commitment format
- **Error (404 Not Found)**: Blob not found on Celestia

#### Example

```bash
# Retrieve a blob by commitment
curl http://localhost:3100/get/0x010c39300000000000001234567890abcdef...

# Response
hello world
```

#### Commitment Parsing

The server accepts commitments in the following formats:
- Full generic commitment: `0x01 0c <blob_id>`
- With version byte: `0x0c <blob_id>`
- Raw blob ID: `<blob_id>`

The blob ID contains:
- `height` (8 bytes, little-endian): Celestia block height
- `commitment` (32 bytes): Shares commitment from blob
- `share_offset` (4 bytes, optional): Start share index
- `share_size` (4 bytes, optional): Number of shares

#### Error Responses

| Status | Description | Cause |
|--------|-------------|-------|
| 400 | Invalid commitment format | Malformed hex or too short |
| 404 | Not found | Blob doesn't exist at height/commitment |

---

### GET /health

Health check endpoint for load balancers and Kubernetes probes.

#### Request

- **Method**: `GET`
- **Path**: `/health`

#### Response

- **Success (200 OK)**: `OK`

#### Example

```bash
curl http://localhost:3100/health
# Response: OK
```

---

## Prometheus Metrics

When metrics are enabled (`--metrics.enabled=true`), Prometheus metrics are available on a separate port (default: 6060).

### Metrics Endpoint

```bash
curl http://localhost:6060/metrics
```

### Available Metrics

#### HTTP Request Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `op_altda_request_duration_seconds` | Histogram | `method` | HTTP request duration |
| `op_altda_blob_size_bytes` | Histogram | - | Blob sizes |
| `op_altda_inclusion_height` | Gauge | - | Latest Celestia height |

#### Celestia Submission Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `celestia_submission_duration_seconds` | Histogram | Time to submit blob |
| `celestia_submissions_total` | Counter | Total submissions |
| `celestia_submission_errors_total` | Counter | Failed submissions |

#### Celestia Retrieval Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `celestia_retrieval_duration_seconds` | Histogram | Time to retrieve blob |
| `celestia_retrievals_total` | Counter | Total retrievals |
| `celestia_retrieval_errors_total` | Counter | Failed retrievals |

---

## Error Handling

### Retry Strategy

For `500 Internal Server Error` responses on `/put`:

1. The server fails immediately on errors
2. The calling client (e.g., `op-batcher`) should implement retry logic
3. Recommended: exponential backoff with jitter
4. Consider increasing timeout if submissions consistently fail

### Common Error Scenarios

| Scenario | Endpoint | Status | Solution |
|----------|----------|--------|----------|
| Empty blob | PUT /put | 400 | Ensure request body is not empty |
| Invalid hex | GET /get | 400 | Check commitment encoding |
| Blob not found | GET /get | 404 | Verify commitment and wait for indexing |
| Network timeout | PUT /put | 500 | Retry; check Celestia node status |
| Insufficient funds | PUT /put | 500 | Fund the signing key with TIA |

---

## Behavioral Notes

### Stateless Design

- **No Caching**: Every GET request fetches from Celestia
- **No Batching**: Each PUT creates exactly one Celestia blob
- **No Background Workers**: All operations are synchronous

### Timing Expectations

| Operation | Typical Duration | Notes |
|-----------|-----------------|-------|
| PUT (submission) | 10-60 seconds | Blocks until Celestia confirmation |
| GET (retrieval) | 100ms - 2s | Depends on Celestia node proximity |
| Health check | < 10ms | No external calls |

### Commitment Validity

- Commitments are valid immediately after PUT returns
- GET may briefly return 404 if Celestia indexing is delayed
- Commitments are permanent once created

---

## Integration Examples

### op-batcher Configuration

Point op-batcher to the DA server:

```bash
op-batcher \
  --da-server=http://localhost:3100 \
  --da-commitment-type=generic \
  ...
```

### curl Examples

```bash
# Submit blob and save commitment
COMMITMENT=$(curl -s -X PUT http://localhost:3100/put \
  -H "Content-Type: application/octet-stream" \
  --data-binary @blob.bin)
echo "Commitment: $COMMITMENT"

# Retrieve blob
curl -s http://localhost:3100/get/$COMMITMENT > retrieved.bin

# Verify roundtrip
diff blob.bin retrieved.bin && echo "Match!"
```

### Go Client Example

```go
package main

import (
    "bytes"
    "fmt"
    "io"
    "net/http"
)

func main() {
    // Submit blob
    data := []byte("hello world")
    resp, _ := http.Post("http://localhost:3100/put", 
        "application/octet-stream", 
        bytes.NewReader(data))
    commitment, _ := io.ReadAll(resp.Body)
    resp.Body.Close()
    
    fmt.Printf("Commitment: %s\n", commitment)
    
    // Retrieve blob
    resp, _ = http.Get("http://localhost:3100/get/" + string(commitment))
    retrieved, _ := io.ReadAll(resp.Body)
    resp.Body.Close()
    
    fmt.Printf("Retrieved: %s\n", retrieved)
}
```

