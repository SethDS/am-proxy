# Agent brief: complete Prometheus `/metrics` for am-proxy

**Audience:** software-engineering agent  
**Repo:** https://github.com/SethDS/am-proxy  
**Branch to work on:** `feature/prometheus-metrics` (already created; brief committed — **do not start a new branch**)  
**Base:** latest `main` at brief time (`14388b9` lineage / PR #1 scaffold)  
**Scope:** harden and finish `/metrics` observability only — **do not** implement WAL, change routing/match logic, or add auth on `/metrics`

Architecture context: `docs/alertmanager-proxy-architecture.md` (also in Project store). Project priorities: reliability → volume → matching → `am-proxy.yml`.

---

## 1. Goal

Expose a scrapable **`GET /metrics`** in Prometheus **text exposition format** that describes am-proxy health and runtime: ingress custody outcomes, queue state, forward success/failure by status-class policy, drops, dead-letters, auth-holds, retries, plus process/Go runtime meters.

Success means operators can scrape the proxy and alert on backpressure (`503` / queue depth), forward failures, and custody loss paths **without** changing alert routing behavior or adding disk WAL.

---

## 2. Current state on `main` (read this first — do not reinvent)

The scaffold **already** has most of this. Your job is to **complete gaps**, add tests/docs, and optionally extend metric coverage — not to replace the existing design unless something is wrong.

| Piece | Status |
|-------|--------|
| `GET /metrics` | **Wired** in `internal/api/server.go` via `promhttp.Handler()` |
| Metric definitions | **Present** in `internal/metrics/metrics.go` (`promauto` → default registry) |
| Hot-path increments | **Mostly wired** in `internal/api`, `internal/custody` (manager/queue/worker) |
| Library | **`github.com/prometheus/client_golang` v1.20.5** already in `go.mod` |
| README endpoints table | Lists `/metrics` but **no scrape/verify section** and **no metric catalog** |
| Dedicated `/metrics` tests | **Missing** |
| Go / process collectors | **Not registered** (default registry only has `promauto` series) |
| Retry counter | **Missing** (retries happen in `custody.worker` / `requeue*` but no dedicated metric) |
| Queue capacity gauge | **Missing** (only `am_proxy_queue_depth`) |
| Ingress HTTP request counter by status | **Missing** (alert-level counters exist; request-level `200`/`400`/`503` not) |

**Do not** rip out `promauto` or invent a parallel metrics package. Prefer extending `internal/metrics` and call sites.

---

## 3. Architecture constraints (must preserve)

From the locked architecture / project context:

1. **`200` = custody accepted** (all surviving alerts enqueued all-or-nothing for that request).  
2. **`503` = backpressure** (queue full or max in-flight) — Prometheus retries; never silent success after drop.  
3. **`400` = bad JSON** — nothing queued.  
4. **Pass-through only** — no payload mutation; metrics observe, they do not alter alerts.  
5. **Per-destination memory custody queues** + workers; drop past **`endsAt`**; backend failure policy:
   - `400` / other poison 4xx → dead-letter  
   - `401`/`403` → auth-hold (retain, stop rapid retries)  
   - `404` → bounded retry  
   - `408` / `5xx` / network → retry + backoff  
   - `429` → hold + `Retry-After`  
   - retry/max-age limit → dead-letter  
   - rule `drop` / unmatched drop → deliberate drop (not queued)  
6. **MVP memory queue** — crash loss is documented; **WAL is out of scope**.  
7. **No auth on `/metrics` for now** (same as ingress MVP).  
8. Metrics increments must stay **cheap** on the hot path (counters/gauges only; no blocking I/O, no label cardinality from alert label values).

Label cardinality rules:

- Prefer `backend` = destination key already used in custody (`DestSpec.Key` / queue key).  
- Prefer `rule` = rule index string or `"unmatched"` (already used).  
- Prefer `reason` = forward/dead-letter reason strings already used (`forward.Outcome.String()`, `"not_found_limit"`, etc.).  
- **Never** put alert label values, fingerprints, or URLs with high cardinality into metric labels.

---

## 4. Suggested metric names

Align with architecture §6 and **existing** `internal/metrics/metrics.go` names (keep existing series stable; add missing ones).

### Already defined (keep names; verify still incremented correctly)

| Name | Type | Labels | Notes |
|------|------|--------|-------|
| `am_proxy_alerts_received_total` | counter | — | Per alert in decoded batch |
| `am_proxy_alerts_dropped_total` | counter | `rule` | Deliberate filter / unmatched drop |
| `am_proxy_alerts_enqueued_total` | counter | `backend` | Custody accepted |
| `am_proxy_alerts_forwarded_total` | counter | `backend` | Successful AM POST |
| `am_proxy_alerts_expired_total` | counter | `backend` | Dropped past `endsAt` |
| `am_proxy_alerts_dead_letter_total` | counter | `backend`, `reason` | Code name (architecture text said `…_dead_lettered_…` — **keep code name**) |
| `am_proxy_alerts_auth_hold_total` | counter | `backend` | 401/403 retain |
| `am_proxy_queue_depth` | gauge | `backend` | Current depth |
| `am_proxy_enqueue_rejections_total` | counter | — | Custody refused → Prometheus saw `503` (queue full **or** max in-flight today) |
| `am_proxy_forward_duration_seconds` | histogram | `backend` | Forward POST latency |
| `am_proxy_forward_errors_total` | counter | `backend`, `reason` | `reason` ≈ outcome class (`bad_request`, `auth`, `retryable`, …) |
| `am_proxy_inflight_requests` | gauge | — | In-flight ingress requests |

### Add (recommended minimum gaps)

| Name | Type | Labels | Where to wire |
|------|------|--------|----------------|
| `am_proxy_http_requests_total` | counter | `code` (`200`/`400`/`503`/`500`) | `internal/api/server.go` `handleAlerts` (and optionally health/ready if cheap; alerts path is required) |
| `am_proxy_queue_capacity` | gauge | `backend` | Set when queue created / config applied (`cfg.Queue.MaxAlertsPerBackend`) |
| `am_proxy_forward_retries_total` | counter | `backend`, `reason` | Increment when `requeue` / `requeueWithPolicy` keeps alerts for another attempt (`internal/custody/worker.go`) |
| `am_proxy_alerts_held_total` | counter | `backend`, `reason` | Optional alias/clarity for rate-limit holds (`429`); auth already has `auth_hold`. If you add this, use `reason=rate_limited` (and do not double-count auth if you keep `auth_hold`) |

### Process / Go runtime

Register once at process start (prefer `cmd/am-proxy/main.go` or a small `metrics.RegisterRuntime()` called from main):

```go
prometheus.MustRegister(
    collectors.NewGoCollector(),
    collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
)
```

Use `github.com/prometheus/client_golang/prometheus/collectors`. This surfaces `go_*` and `process_*` series via the same `/metrics` handler.

### Optional (nice-to-have, still in scope if cheap)

- Split enqueue rejection reasons: label `reason=queue_full|max_inflight` on `am_proxy_enqueue_rejections_total` (**breaking label add** — if you add a label, migrate carefully or introduce a new vec and deprecate the unlabeled counter in comments; prefer a **new** `am_proxy_ingress_rejected_total{reason=}` and keep the old counter incremented too for compatibility, **or** only document the unlabeled counter and leave as-is).
- `am_proxy_dead_letter_depth` / `am_proxy_auth_hold_depth` gauges if dead-letter/auth retain slices are inspectable in `custody.Manager`.

---

## 5. Library preference

**Use `prometheus/client_golang`** (already required). Keep `promhttp.Handler()` on `/metrics`. Keep `promauto` unless you need a custom registry for tests — for tests, either:

- scrape `httptest` against `Server.Handler()` and assert substrings / parse with `expfmt`, or  
- use a dedicated registry in new code paths only if you must isolate tests (do not break production default registry used by existing promauto vars).

Do **not** add OpenTelemetry metrics exporters or a second metrics stack for MVP.

---

## 6. Where to wire in the Go tree

Module: `github.com/SethDS/am-proxy`

```
cmd/am-proxy/main.go          # lifecycle; register Go/process collectors here
internal/api/server.go        # GET /metrics (exists); ingress HTTP counters
internal/api/server_test.go   # add /metrics scrapability + counter assertions
internal/metrics/metrics.go   # define/extend series
internal/custody/manager.go   # enqueue / dead-letter / auth-hold (already increments)
internal/custody/queue.go     # queue_depth (exists); add capacity gauge updates
internal/custody/worker.go    # forward success/error/duration (exists); add retries
internal/forward/client.go    # classify outcomes only — prefer counting in custody, not forward
internal/match/…              # do not change matching for metrics
internal/config/…             # read capacity for gauges only; no schema change required
```

**Handler mapping today** (`internal/api/server.go`):

- `POST /api/v2/alerts` → custody path  
- `GET /-/healthy`, `GET /-/ready`  
- `GET /metrics` → `promhttp.Handler()`

Keep that surface. Do not move `/metrics` behind auth.

---

## 7. Acceptance criteria

1. **Scrapable:** `GET /metrics` returns HTTP 200 and Prometheus exposition text including at least the existing `am_proxy_*` series after traffic, plus `go_*` / `process_*` after you register collectors.  
2. **Correct under custody semantics:** posting alerts that enqueue increments received/enqueued; drops increment dropped; full queue / max in-flight yields `503` and rejection counter; forwards / expires / dead-letters / auth-holds continue to increment as today (add tests that assert metric text or registry values, not only HTTP codes).  
3. **Tests:** unit and/or httptest integration covering `/metrics` body (e.g. contains `am_proxy_alerts_received_total`) and at least one path that proves a counter moves after `POST /api/v2/alerts`. Prefer extending `internal/api/server_test.go` and/or `internal/metrics` tests. `go test ./...` passes.  
4. **README:** short “Metrics” subsection: scrape URL, example `curl`, pointer that series are `am_proxy_*`, note that `/metrics` is unauthenticated in MVP.  
5. **Hot path:** no new locks beyond existing custody locks; no per-alert string alloc explosion for labels; histogram observe only on forward attempts (already true).  
6. **No behavior change** to routing, match evaluation, enqueue all-or-nothing, or failure policy — metrics only.

---

## 8. Out of scope

- Auth / TLS / basic-auth / network policy for `/metrics`  
- Changing match/route rules, `am-proxy.yml` schema (except docs)  
- Disk WAL / crash-safe queue  
- Grafana dashboard JSON (optional later)  
- Renaming existing metric series in a breaking way  
- Fail-open semantics (explicitly superseded — do not reintroduce)

---

## 9. How to verify

```bash
# From repo root on feature/prometheus-metrics
go test ./...
go build -o am-proxy ./cmd/am-proxy
./am-proxy --config.file=am-proxy.yml --listen-address=:9094

# Scrape
curl -sS localhost:9094/metrics | head
curl -sS localhost:9094/metrics | grep '^am_proxy_'

# Drive ingress then re-scrape (example empty drop/route depends on config)
curl -sS -X POST localhost:9094/api/v2/alerts \
  -H 'Content-Type: application/json' \
  -d '[{"labels":{"alertname":"HighCPU","severity":"warning"}}]'
curl -sS localhost:9094/metrics | grep am_proxy_alerts_received_total

# Optional lint of exposition format if promtool is installed
curl -sS localhost:9094/metrics | promtool check metrics
```

Manual checks:

- After a deliberate drop rule, `am_proxy_alerts_dropped_total` increases.  
- With workers stopped / tiny queue, second burst returns `503` and rejection counter rises.  
- `go_*` / `process_*` appear once collectors are registered.

---

## 10. Implementation order (suggested)

1. Register Go/process collectors; confirm `/metrics` still serves.  
2. Add `am_proxy_http_requests_total{code=}`, `am_proxy_queue_capacity`, `am_proxy_forward_retries_total`.  
3. Wire increments at the call sites listed above.  
4. Add httptest `/metrics` tests + one counter-movement test.  
5. README Metrics subsection.  
6. `go test ./...`; manual `curl` scrape.  
7. Push commits to **`feature/prometheus-metrics`** and update the existing draft PR description with what you implemented.

---

## 11. PR / branch notes for the SE agent

- Branch **`feature/prometheus-metrics`** already exists with this brief at `docs/metrics-endpoint-agent-brief.md`.  
- Implement on that branch; push further commits; mark PR ready when acceptance criteria are met.  
- Keep commits focused on metrics/observability.
