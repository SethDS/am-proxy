# am-proxy

Go proxy between Prometheus and one or more Alertmanager instances.

Prometheus posts alerts to the proxy; rules in `am-proxy.yml` match on **any alert property** (dotted paths, exact or regex), then **drop** or **route**. The proxy takes **custody** of alerts (in-memory queue + retry) so bursts and backend failures do not silently drop them.

## Semantics

| Response | Meaning |
|----------|---------|
| `200` | Proxy accepted **custody** (alerts enqueued). Forwarding happens asynchronously. |
| `400` | Malformed JSON body. |
| `503` | Queues full / too many in-flight requests — Prometheus should retry. |

**MVP durability:** queues are **memory-only**. A process crash loses in-flight custody. Disk WAL is planned for a later phase.

## Quick start

```bash
go run ./cmd/am-proxy --config.file=am-proxy.yml
```

Point Prometheus at the proxy:

```yaml
alerting:
  alertmanagers:
    - static_configs:
        - targets: ["am-proxy:9094"]
```

## Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/api/v2/alerts` | Alertmanager v2 ingress |
| `GET` | `/-/healthy` | Liveness |
| `GET` | `/-/ready` | Readiness |
| `GET` | `/metrics` | Prometheus metrics |

No ingress auth in MVP (use network policy / private mesh).

## Metrics

`GET /metrics` serves Prometheus exposition format (unauthenticated in MVP — restrict with network policy). Series are prefixed `am_proxy_*`, plus standard `go_*` / `process_*` runtime collectors.

```bash
curl -sS localhost:9094/metrics | grep '^am_proxy_'
```

Useful series include ingress (`am_proxy_alerts_received_total`, `am_proxy_http_requests_total`), custody (`am_proxy_queue_depth`, `am_proxy_queue_capacity`, `am_proxy_enqueue_rejections_total`), drops/expiry/dead-letter/auth-hold, and forward latency/errors/retries. See `docs/metrics-endpoint-agent-brief.md` for the full catalog.

## Matching & routing

Rules in `am-proxy.yml` are evaluated in order:

1. First matching `action: drop` → drop (metric) and stop.
2. Matching `action: route` → enqueue for that backend or inline `url`.
3. Default is **first match wins**; set `continue: true` to fan-out to additional routes.
4. If nothing matches → `unmatched: default` (named `default_backend`) or `drop`.

Matchers use dotted paths (`labels.severity`, `annotations.summary`, `generatorURL`, …) with `op: exact` or `op: regex` (anchored full-string match). All matchers on a rule are AND.

See `am-proxy.yml` and `examples/pass-through.yml`.

## Custody & failure policy

- Queued alerts past `endsAt` are dropped (`am_proxy_alerts_expired_total`).
- Outbound batches default to 200 alerts; workers retry with exponential backoff + jitter.
- Backend HTTP outcomes:

| Status / case | Behavior |
|---------------|----------|
| `400` / other poison 4xx | Dead-letter |
| `401` / `403` | Stop rapid retries; log for ops; retain for recovery |
| `404` | Bounded retries (`retry_404_max`) after config check |
| `408` | Exponential backoff + jitter |
| `429` | Hold; respect `Retry-After` |
| `5xx` / network | Retry with backoff |
| Retry limit / max age | Dead-letter |
| Deliberate filter (`drop`) | Drop + metrics |

## Flags

- `--config.file=am-proxy.yml`
- `--listen-address=:9094` (overrides config `listen`)

## Develop

```bash
go test ./...
go build -o am-proxy ./cmd/am-proxy
```

Module: `github.com/SethDS/am-proxy`
