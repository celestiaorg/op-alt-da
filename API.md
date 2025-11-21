# API Reference

This document describes the HTTP API endpoints exposed by the Celestia Alt-DA server.

## Base URL

The server listens on `http://{addr}:{port}` (default: `http://127.0.0.1:3100`)

---

## Endpoints

### PUT /put

Store a blob and receive a commitment.

**Request:**
- Method: `POST`
- Content-Type: `application/octet-stream`
- Body: Raw blob data (binary)

**Response:**
- Status: `200 OK`
- Body: Commitment string in format `0x0c<commitment_hex>`
  - `0x` - Prefix
  - `0c` - Version byte
  - `<commitment_hex>` - 64 hex characters (32 bytes)

**Example:**
```bash
curl -X POST http://localhost:3100/put \
  -H "Content-Type: application/octet-stream" \
  --data-binary @blob.bin

# Response:
# 0x0c1234567890abcdef...
```

**Error Responses:**
- `400 Bad Request` - Empty blob data
- `500 Internal Server Error` - Failed to compute commitment or store blob

---

### GET /get/{commitment}

Retrieve a blob by its commitment.

**Request:**
- Method: `GET`
- URL Parameter: `{commitment}` - Can include or exclude `0x` prefix and version byte

**Response:**
- Status: `200 OK`
- Body: Raw blob data (binary)

**Example:**
```bash
# With version byte
curl http://localhost:3100/get/0x0c1234567890abcdef...

# Without version byte
curl http://localhost:3100/get/0x1234567890abcdef...

# Without 0x prefix
curl http://localhost:3100/get/1234567890abcdef...
```

**Error Responses:**
- `400 Bad Request` - Invalid commitment format
- `404 Not Found` - Blob not found
- `500 Internal Server Error` - Database error

---

### GET /health

Health check endpoint.

**Request:**
- Method: `GET`

**Response:**
- Status: `200 OK`
- Body: `OK`

**Example:**
```bash
curl http://localhost:3100/health

# Response:
# OK
```

---

### GET /stats

Get database statistics showing blob and batch counts by status.

**Request:**
- Method: `GET`

**Response:**
- Status: `200 OK`
- Content-Type: `application/json`
- Body: JSON object with counts

**Response Schema:**
```json
{
  "blob_counts": {
    "pending_submission": 0,
    "batched": 0,
    "confirmed": 0
  },
  "batch_counts": {
    "pending_submission": 0,
    "submitted": 0,
    "confirmed": 0
  }
}
```

**Example:**
```bash
curl http://localhost:3100/stats

# Response:
# {
#   "blob_counts": {
#     "pending_submission": 42,
#     "batched": 15,
#     "confirmed": 1203
#   },
#   "batch_counts": {
#     "pending_submission": 3,
#     "submitted": 1,
#     "confirmed": 89
#   }
# }
```

**Error Responses:**
- `500 Internal Server Error` - Failed to query database

---

## Blob Status Lifecycle

Blobs transition through the following statuses:

1. **pending_submission** - Blob stored in database, waiting to be batched
2. **batched** - Blob packed into a batch and submitted to Celestia
3. **confirmed** - Blob confirmed on Celestia (has `celestia_height` set)

## Batch Status Lifecycle

Batches transition through the following statuses:

1. **pending_submission** - Batch created, not yet submitted to Celestia
2. **submitted** - Batch submitted to Celestia, waiting for confirmation
3. **confirmed** - Batch confirmed on Celestia (has `celestia_height` set)

## Notes

- All endpoints support concurrent access
- PUT operations return immediately after database write (~50ms)
- GET operations read from local database (~10ms)
- Commitments are deterministic - same data always produces same commitment
- The server handles commitment version byte parsing automatically
