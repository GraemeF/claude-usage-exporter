# claude-usage-exporter

Prometheus and OpenTelemetry exporter for Claude.ai session and weekly usage
metrics. Polls the Claude.ai usage API for each configured account and exposes
utilization percentages and reset timestamps as metrics.

## How it works

The exporter reads account credentials from `accounts.yaml`, then polls the
Claude.ai usage API for each account on independent goroutines. Results are
exposed as OpenTelemetry metrics via a Prometheus `/metrics` endpoint (and
optionally pushed over OTLP).

### Adaptive polling

Poll frequency adjusts automatically based on activity:

| State | Behaviour |
|-------|-----------|
| **Active** | Polls every `activeInterval` (default 30 s) whenever usage values change between polls |
| **Idle back-off** | After `idleThreshold` (default 3) consecutive unchanged polls, the interval doubles on each poll up to `idleInterval` (default 5 m) |
| **Reset burst** | When a usage window reset is less than `resetBurstWindow` (default 2 m) away, the interval snaps back to `activeInterval` regardless of idle state |

Any change in the session or weekly utilization value immediately resets the
interval to `activeInterval`.

## Metrics

All metrics carry an `account` label (the account name from your config) and an
`org_id` label (the organization ID). The `org_id` is the join key between these
metrics and Claude Code OTEL metrics, which emit `organization.id` as a resource
attribute — enabling correlation of which CC host is consuming which account's
quota.

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `claude_usage_session_utilization` | Gauge | % | 5-hour session window utilization (0–100) |
| `claude_usage_weekly_utilization` | Gauge | % | 7-day weekly utilization (0–100) |
| `claude_usage_session_reset` | Gauge | seconds | Unix timestamp when the 5-hour session window resets |
| `claude_usage_weekly_reset` | Gauge | seconds | Unix timestamp when the 7-day weekly limit resets |
| `claude_usage_poll_last_success` | Gauge | seconds | Unix timestamp of the last successful poll |
| `claude_usage_poll_interval` | Gauge | seconds | Current adaptive poll interval |
| `claude_usage_poll_errors_total` | Counter | | Total number of failed polls |

## OTLP support

The Prometheus pull endpoint is always enabled. To also push metrics over OTLP
(gRPC), set the standard OpenTelemetry environment variable:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
```

Any other `OTEL_EXPORTER_OTLP_*` variables (headers, TLS, etc.) are respected
by the underlying Go OTLP SDK.

## Configuration

Create an `accounts.yaml` (see `accounts.example.yaml`):

```yaml
# Tuning (all optional — defaults shown):
activeInterval: 30s    # poll interval when usage is changing
idleInterval: 5m       # max poll interval when usage is stable
idleThreshold: 3       # unchanged polls before back-off starts
resetBurstWindow: 2m   # snap to activeInterval when a reset is this close
listenAddr: ":9091"    # Prometheus /metrics listen address

accounts:
  - name: personal
    orgId: "your-org-id"            # from claude.ai cookies / network requests
    sessionKey: "sk-ant-sid..."     # sessionKey cookie value

  - name: work
    orgId: "your-work-org-id"
    sessionKey: "sk-ant-sid..."
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ACCOUNTS_FILE` | `accounts.yaml` | Path to the accounts config file |
| `LISTEN_ADDR` | `:9091` | Override `listenAddr` from config |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | *(unset)* | Enable OTLP push exporter |

## Quickstart (Docker Compose)

The fastest way to get a working stack with Prometheus and Grafana:

1. Copy `accounts.example.yaml` to `accounts.yaml` and fill in your credentials:

   ```bash
   cp accounts.example.yaml accounts.yaml
   # Edit accounts.yaml with your orgId and sessionKey values
   ```

2. Start the stack:

   ```bash
   docker compose up -d
   ```

3. Open Grafana at [http://localhost:3000](http://localhost:3000) — the Claude
   Usage dashboard is pre-provisioned with no login required.

   Prometheus is available at [http://localhost:9090](http://localhost:9090) and
   the exporter metrics at [http://localhost:9091/metrics](http://localhost:9091/metrics).

To stop: `docker compose down` (add `-v` to also remove stored data).

## Usage

### Nix

```bash
# Run directly
nix run github:GraemeF/claude-usage-exporter

# Build
nix build github:GraemeF/claude-usage-exporter

# Dev shell (Go toolchain)
nix develop github:GraemeF/claude-usage-exporter
```

### Docker

Build and load the image with Nix:

```bash
nix build .#dockerImage
docker load < result
```

Run:

```bash
docker run -d \
  -v /path/to/accounts.yaml:/config/accounts.yaml:ro \
  -p 9091:9091 \
  ghcr.io/graemef/claude-usage-exporter:latest
```

With OTLP enabled:

```bash
docker run -d \
  -v /path/to/accounts.yaml:/config/accounts.yaml:ro \
  -p 9091:9091 \
  -e OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317 \
  ghcr.io/graemef/claude-usage-exporter:latest
```

### Endpoints

| Path | Description |
|------|-------------|
| `/metrics` | Prometheus metrics |
| `/healthz` | Health check (HTTP 200) |

## Grafana dashboard

A pre-built dashboard is included at `grafana/claude-usage-exporter.json`. Import
it into Grafana and select your Prometheus data source.

The dashboard includes:

- Session and weekly utilization time series
- Poll errors and adaptive interval panels
- **CC OTEL Correlation panel** — a table joining usage exporter metrics with
  Claude Code OTEL host metrics via `org_id` = `organization_id`, showing which
  CC host is consuming which account's quota
- Bar gauges keyed by `org_id` for at-a-glance quota status
