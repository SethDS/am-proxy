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

## Install as a binary (systemd / RHEL)

Build a static linux/amd64 binary, install config + unit, run as a dedicated non-root user:

```bash
# Build (from a machine with Go 1.22+)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o am-proxy ./cmd/am-proxy

# On the target host
sudo useradd --system --home-dir /var/lib/am-proxy --create-home --shell /sbin/nologin am-proxy
sudo install -d -o root -g am-proxy -m 0755 /etc/am-proxy
sudo install -o root -g am-proxy -m 0640 am-proxy.yml /etc/am-proxy/am-proxy.yml   # or examples/pass-through.yml
sudo install -o root -g root -m 0755 am-proxy /usr/local/bin/am-proxy
sudo install -o root -g root -m 0644 deploy/am-proxy.service /etc/systemd/system/am-proxy.service

sudo systemctl daemon-reload
sudo systemctl enable --now am-proxy
curl -sf http://127.0.0.1:9094/-/healthy
```

Listen address comes from `listen` in `/etc/am-proxy/am-proxy.yml` (default `:9094`). Optional firewalld:

```bash
sudo firewall-cmd --add-port=9094/tcp --permanent && sudo firewall-cmd --reload
```

Unit file: `deploy/am-proxy.service`. For containers instead, see below.

## Container image (Docker / Podman)

Multi-stage `Dockerfile` builds a static binary and runs it as non-root on **UBI 9 minimal** (`linux/amd64`). **aarch64** is a future target — pass `--platform linux/amd64` on arm hosts for now.

| Item | Value |
|------|--------|
| Listen port | **9094** (`EXPOSE 9094`; config `listen` / `--listen-address`) |
| Default config in image | `/etc/am-proxy/am-proxy.yml` |
| Mount path (recommended) | `/etc/am-proxy/am-proxy.yml` |
| Alternate mount | `/config/am-proxy.yml` (pass `--config.file=/config/am-proxy.yml`) |
| User | UID/GID **10001** (non-root) |

### Build

```bash
# Docker
docker build --platform linux/amd64 -t am-proxy:local .

# Podman (same Dockerfile works)
podman build --platform linux/amd64 -t am-proxy:local .
```

### Run (Docker)

```bash
docker run --rm -p 9094:9094 \
  -v "$(pwd)/examples/container/am-proxy.yml:/etc/am-proxy/am-proxy.yml:ro" \
  am-proxy:local
```

### Run on RHEL with Podman

```bash
# Build
podman build --platform linux/amd64 -t am-proxy:local .

# Run with SELinux-friendly volume label (:Z) and published port
podman run --rm -p 9094:9094 \
  -v /etc/am-proxy/am-proxy.yml:/etc/am-proxy/am-proxy.yml:ro,Z \
  am-proxy:local

# Or mount under /config and point the flag
podman run --rm -p 9094:9094 \
  -v /path/to/am-proxy.yml:/config/am-proxy.yml:ro,Z \
  am-proxy:local --config.file=/config/am-proxy.yml
```

Open the port on the host if firewalld is enabled:

```bash
sudo firewall-cmd --add-port=9094/tcp --permanent
sudo firewall-cmd --reload
```

Quick checks:

```bash
curl -sf http://127.0.0.1:9094/-/healthy
curl -sf http://127.0.0.1:9094/-/ready
```

Edit mounted `am-proxy.yml` so `backends.*.urls` reach Alertmanagers from the container network (not `127.0.0.1` unless you use host networking).

### Compose (local smoke)

Proxy-only compose file — no fake Alertmanager:

```bash
docker compose up --build
# or: podman compose up --build
```

See `compose.yaml` and `examples/container/am-proxy.yml`.

## Develop

```bash
go test ./...
go build -o am-proxy ./cmd/am-proxy
```

Module: `github.com/SethDS/am-proxy`
