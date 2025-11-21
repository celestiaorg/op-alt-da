# Metrics Guide

This document explains the available metrics and how to use them for monitoring your Celestia DA server.

## Overview

The DA server exposes two levels of metrics:
1. **HTTP-level metrics** - Track client-facing API performance (compatible with main branch dashboards)
2. **Worker-level metrics** - Track internal Celestia operations (submission and retrieval)

All metrics are collected by a **single shared instance** passed to all workers, meaning the worker-level metrics aggregate data from:
- Submission Worker (batching and submitting to Celestia)
- Event Listener (reconciliation via Get operations)

## Enabling Metrics

Start the server with metrics enabled:

```bash
./bin/da-server \
  --celestia.server http://localhost:26658 \
  --celestia.auth-token $AUTH_TOKEN \
  --celestia.namespace 00000000000000000000000000000000000000000008e5f679bf7116cb \
  --db.data-dir ./da_data \
  --metrics.enabled \
  --metrics.port 26661
```

Metrics will be available at: `http://localhost:26661/metrics`

## Available Metrics

### HTTP-Level Metrics (Client-facing)

These metrics track the performance of the HTTP API endpoints from the client's perspective.

| Metric | Type | Description | Labels |
|--------|------|-------------|--------|
| `op_altda_request_duration_seconds` | Histogram | Duration of HTTP requests (PUT/GET) | `method` (get, put) |
| `op_altda_blob_size_bytes` | Histogram | Size of blobs submitted via PUT | - |
| `op_altda_inclusion_height` | Gauge | Latest Celestia block height seen during GET | - |

**Buckets for request_duration:** 0.005s, 0.01s, 0.025s, 0.05s, 0.1s, 0.25s, 0.5s, 1s, 2.5s, 5s, 10s

**Buckets for blob_size:** 1KB, 4KB, 16KB, 64KB, 256KB, 1MB, 4MB, 8MB

### Worker-Level Metrics (Internal Operations)

These metrics track the internal operations with Celestia DA layer, aggregated across all workers.

#### Submission Metrics

Track batch submissions to Celestia (from Submission Worker).

| Metric | Type | Description |
|--------|------|-------------|
| `celestia_submission_duration_seconds` | Histogram | Time to submit batch to Celestia |
| `celestia_submission_size_bytes` | Histogram | Size of batch data submitted |
| `celestia_submissions_total` | Counter | Total successful submissions |
| `celestia_submission_errors_total` | Counter | Total failed submissions |

**Buckets for submission_duration:** 0.1s, 0.5s, 1s, 2s, 5s, 10s, 15s, 30s, 60s, 120s, 300s

**Buckets for submission_size:** 1KB, 10KB, 100KB, 500KB, 1MB, 5MB, 10MB

#### Retrieval Metrics

Track blob retrievals from Celestia (from Event Listener during reconciliation).

| Metric | Type | Description |
|--------|------|-------------|
| `celestia_retrieval_duration_seconds` | Histogram | Time to retrieve blob from Celestia |
| `celestia_retrieval_size_bytes` | Histogram | Size of blob data retrieved |
| `celestia_retrievals_total` | Counter | Total successful retrievals |
| `celestia_retrieval_errors_total` | Counter | Total failed retrievals |

**Buckets for retrieval_duration:** 0.01s, 0.05s, 0.1s, 0.5s, 1s, 2s, 5s, 10s, 30s, 60s

**Buckets for retrieval_size:** 1KB, 10KB, 100KB, 500KB, 1MB, 5MB, 10MB

## Grafana Dashboard Queries

### API Performance Panel

**Average PUT latency (per minute):**
```promql
rate(op_altda_request_duration_seconds_sum{method="put"}[1m])
/
rate(op_altda_request_duration_seconds_count{method="put"}[1m])
```

**Average GET latency (per minute):**
```promql
rate(op_altda_request_duration_seconds_sum{method="get"}[1m])
/
rate(op_altda_request_duration_seconds_count{method="get"}[1m])
```

**Request rate (requests/second):**
```promql
sum(rate(op_altda_request_duration_seconds_count[1m])) by (method)
```

**P95 GET latency:**
```promql
histogram_quantile(0.95,
  rate(op_altda_request_duration_seconds_bucket{method="get"}[5m])
)
```

**P99 PUT latency:**
```promql
histogram_quantile(0.99,
  rate(op_altda_request_duration_seconds_bucket{method="put"}[5m])
)
```

### Blob Size Distribution Panel

**Average blob size (per minute):**
```promql
rate(op_altda_blob_size_bytes_sum[1m])
/
rate(op_altda_blob_size_bytes_count[1m])
```

**Blob size percentiles:**
```promql
histogram_quantile(0.50, rate(op_altda_blob_size_bytes_bucket[5m]))  # P50
histogram_quantile(0.95, rate(op_altda_blob_size_bytes_bucket[5m]))  # P95
histogram_quantile(0.99, rate(op_altda_blob_size_bytes_bucket[5m]))  # P99
```

### Celestia Operations Panel

**Submission success rate (%):**
```promql
(
  rate(celestia_submissions_total[5m])
  /
  (rate(celestia_submissions_total[5m]) + rate(celestia_submission_errors_total[5m]))
) * 100
```

**Submission rate (batches/minute):**
```promql
rate(celestia_submissions_total[1m]) * 60
```

**Average submission latency:**
```promql
rate(celestia_submission_duration_seconds_sum[1m])
/
rate(celestia_submission_duration_seconds_count[1m])
```

**P95 submission latency:**
```promql
histogram_quantile(0.95,
  rate(celestia_submission_duration_seconds_bucket[5m])
)
```

**Retrieval success rate (%):**
```promql
(
  rate(celestia_retrievals_total[5m])
  /
  (rate(celestia_retrievals_total[5m]) + rate(celestia_retrieval_errors_total[5m]))
) * 100
```

**Average batch size submitted:**
```promql
rate(celestia_submission_size_bytes_sum[1m])
/
rate(celestia_submission_size_bytes_count[1m])
```

### Celestia Height Panel

**Current inclusion height:**
```promql
op_altda_inclusion_height
```

**Height growth rate (blocks/minute):**
```promql
rate(op_altda_inclusion_height[1m]) * 60
```

### Error Rate Panel

**Submission error rate (errors/minute):**
```promql
rate(celestia_submission_errors_total[1m]) * 60
```

**Retrieval error rate (errors/minute):**
```promql
rate(celestia_retrieval_errors_total[1m]) * 60
```

**Total error rate (combined):**
```promql
sum(rate(celestia_submission_errors_total[1m]) + rate(celestia_retrieval_errors_total[1m])) * 60
```

## Sample Grafana Dashboard JSON

Here's a basic dashboard structure you can import:

```json
{
  "dashboard": {
    "title": "Celestia DA Server",
    "panels": [
      {
        "title": "Request Latency (P95)",
        "targets": [
          {
            "expr": "histogram_quantile(0.95, rate(op_altda_request_duration_seconds_bucket[5m]))",
            "legendFormat": "{{method}}"
          }
        ],
        "type": "graph"
      },
      {
        "title": "Request Rate",
        "targets": [
          {
            "expr": "sum(rate(op_altda_request_duration_seconds_count[1m])) by (method)",
            "legendFormat": "{{method}}"
          }
        ],
        "type": "graph"
      },
      {
        "title": "Submission Success Rate",
        "targets": [
          {
            "expr": "(rate(celestia_submissions_total[5m]) / (rate(celestia_submissions_total[5m]) + rate(celestia_submission_errors_total[5m]))) * 100",
            "legendFormat": "Success %"
          }
        ],
        "type": "gauge"
      },
      {
        "title": "Celestia Operations Latency",
        "targets": [
          {
            "expr": "rate(celestia_submission_duration_seconds_sum[1m]) / rate(celestia_submission_duration_seconds_count[1m])",
            "legendFormat": "Submission"
          },
          {
            "expr": "rate(celestia_retrieval_duration_seconds_sum[1m]) / rate(celestia_retrieval_duration_seconds_count[1m])",
            "legendFormat": "Retrieval"
          }
        ],
        "type": "graph"
      },
      {
        "title": "Error Rates",
        "targets": [
          {
            "expr": "rate(celestia_submission_errors_total[1m]) * 60",
            "legendFormat": "Submission Errors/min"
          },
          {
            "expr": "rate(celestia_retrieval_errors_total[1m]) * 60",
            "legendFormat": "Retrieval Errors/min"
          }
        ],
        "type": "graph"
      }
    ]
  }
}
```

## Alerting Rules

Example Prometheus alerting rules:

```yaml
groups:
  - name: celestia_da_alerts
    rules:
      # High error rate alert
      - alert: HighSubmissionErrorRate
        expr: |
          (
            rate(celestia_submission_errors_total[5m])
            /
            (rate(celestia_submissions_total[5m]) + rate(celestia_submission_errors_total[5m]))
          ) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High submission error rate detected"
          description: "Submission error rate is {{ $value | humanizePercentage }} over the last 5 minutes"

      # Slow submissions
      - alert: SlowSubmissions
        expr: |
          histogram_quantile(0.95,
            rate(celestia_submission_duration_seconds_bucket[5m])
          ) > 30
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Slow Celestia submissions detected"
          description: "P95 submission latency is {{ $value }}s"

      # API latency alert
      - alert: HighAPILatency
        expr: |
          histogram_quantile(0.95,
            rate(op_altda_request_duration_seconds_bucket[5m])
          ) > 2
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High API latency detected"
          description: "P95 {{ $labels.method }} latency is {{ $value }}s"

      # No submissions happening
      - alert: NoSubmissions
        expr: rate(celestia_submissions_total[10m]) == 0
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: "No submissions to Celestia"
          description: "No successful submissions in the last 15 minutes"
```

## Monitoring Best Practices

1. **Set up alerts** for high error rates (>10% for 5+ minutes)
2. **Monitor P95/P99 latencies** instead of averages to catch tail latencies
3. **Track success rates** for both submissions and retrievals
4. **Watch inclusion height** to ensure Celestia node is syncing
5. **Set baseline alerts** after observing normal operations for a week
6. **Correlate metrics** - if submission latency spikes, check Celestia node health

## Troubleshooting with Metrics

### High PUT latency but normal submission latency
- Issue: Database or commitment computation slow
- Check: Database disk I/O, CPU usage

### High submission error rate
- Issue: Celestia node connectivity or gas issues
- Check: `celestia_submission_errors_total` increasing, node logs

### High retrieval error rate
- Issue: Blobs not found or node syncing issues
- Check: `celestia_retrieval_errors_total`, inclusion height vs. current Celestia height

### Request rate drops to zero
- Issue: Upstream service stopped sending data
- Check: Network connectivity, upstream service health

### Large variance in blob sizes
- Issue: Batching not working efficiently
- Check: `op_altda_blob_size_bytes` distribution, batch configuration
