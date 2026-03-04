# Design: Smart Account Recommendation Engine

**Issue**: cue-1cj
**Author**: polecat/quartz
**Date**: 2026-03-04

## Problem

Users with multiple Claude accounts waste quota when a window resets with
unused capacity. There is no guidance on which account to use at any given
moment to maximise total utilisation across all accounts.

## Available Data

The exporter already collects everything needed per account (every 30s active,
up to 5m idle):

| Metric | Description |
|--------|-------------|
| `claude_usage_session_utilization_percent` | 5-hour window usage (0-100%) |
| `claude_usage_weekly_utilization_percent` | 7-day window usage (0-100%) |
| `claude_usage_session_reset_seconds` | Unix timestamp: session window reset |
| `claude_usage_weekly_reset_seconds` | Unix timestamp: weekly window reset |
| `claude_usage_poll_errors_total` | Error count (health signal) |

Labels: `account`, `org_id`.

No new API calls or data sources are required.

## Recommended Policy: Waste-Minimising Priority Score

### Core Insight

**Waste** = unused capacity that vanishes at reset. The optimal strategy is:
1. Burn accounts approaching reset (their unused capacity is about to disappear)
2. Among non-urgent accounts, prefer the one with the most headroom

### Scoring Function

For each account, compute a priority score. **Highest score = recommended account.**

```
effective_headroom = min(100 - session_util, 100 - weekly_util)

if effective_headroom <= 0:
    score = -1                          # Exhausted — do not use

elif time_to_session_reset < BURN_THRESHOLD and session_headroom > 0:
    score = 1000 + session_headroom     # Burn phase: always prioritised

elif time_to_weekly_reset < BURN_THRESHOLD and weekly_headroom > 0:
    score = 1000 + weekly_headroom      # Burn phase: weekly variant

else:
    score = effective_headroom          # Normal: prefer most headroom
    score += freshness_bonus            # Mild tie-breaker (see below)

if recent_poll_errors > 0:
    score *= 0.5                        # Unhealthy account penalty
```

**Constants:**
- `BURN_THRESHOLD` = 30 minutes (configurable in accounts.yaml)
- `freshness_bonus` = `min(time_to_session_reset / 18000, 1.0) * 10` — a 0-10
  point bonus favouring accounts further from reset (preserves fresh resets for
  later)

### Why This Works

| Scenario | Behaviour |
|----------|-----------|
| Account A: 40% used, resets in 20m | Score = 1060 (burn phase) |
| Account B: 10% used, resets in 4h | Score = 90 + 8 = 98 |
| Account C: 95% used, resets in 20m | Score = 1005 (burn, but less headroom) |
| Account D: 100% session, 50% weekly | Score = -1 (exhausted) |

Result: **A is recommended** (most headroom in burn phase), then B (most
headroom overall). C is burn-phase but nearly exhausted. D is skipped entirely.

### Binding Window Detection

Each account has two independent rate-limit windows. The **binding window** is
whichever will exhaust first:

- If `session_util > weekly_util` → session is binding
- If `weekly_util > session_util` → weekly is binding

The `effective_headroom = min(session, weekly)` automatically selects the
binding constraint. The burn phase checks both windows independently because
either reset is an opportunity to recover capacity.

## Edge Cases

### Multiple accounts resetting simultaneously

When several accounts are in burn phase, they all score > 1000. Ties are broken
by headroom (`1000 + headroom`), so the account with the most remaining capacity
is recommended — maximising value extracted from the limited burn window.

### All accounts near exhaustion

All accounts score close to 0 (or -1 if fully exhausted). The recommendation
still selects the best available option. The dashboard should show a
"critically low" warning when the best score is below a threshold (e.g. < 5).

### Accounts with very different limits

The claude.ai API returns utilisation as a percentage (0-100), which already
normalises across different plan tiers. No adjustment needed.

### Account with stale/errored data

The 0.5x health penalty pushes unhealthy accounts down the ranking. If ALL
accounts have errors, the recommendation is suppressed with a "data unavailable"
indicator rather than guessing.

### Account just reset (0% utilisation)

A freshly-reset account scores 100 + freshness_bonus ≈ 110. It becomes the
natural default until another account enters burn phase.

### Single account

The engine degrades gracefully: one account always gets recommended. The burn
phase alerts are still useful to signal "use it now before reset".

## Dashboard Representation

### Option A: Recommended Account Indicator (Proposed)

Add a new row to the existing Grafana dashboard with three panels:

```
┌───────────────────────────┬───────────────────────────────────┐
│  USE NOW                  │  Account Priority Ranking         │
│  ┌─────────────────────┐  │                                   │
│  │    personal          │  │  1. personal   ███████████ 98    │
│  │    Score: 98         │  │  2. work       ██████     61     │
│  │    Headroom: 88%     │  │  3. team       ██         22     │
│  │    Resets in: 3h 12m │  │  4. backup     (exhausted)       │
│  └─────────────────────┘  │                                   │
├───────────────────────────┼───────────────────────────────────┤
│  Traffic Lights            │  Recommendation Score Over Time  │
│  ● personal  (green)      │  ~~~~/\~~~~/\~~~~~/\~~~~~        │
│  ● work      (yellow)     │  ──/──\──/──\───/──\────        │
│  ● team      (red)        │                                   │
│  ○ backup    (grey/off)   │                                   │
└───────────────────────────┴───────────────────────────────────┘
```

**Panel 1 — "Use Now" (Stat panel)**
- Shows the recommended account name
- Subtitle: effective headroom + time to next reset
- Colour thresholds: green (score > 50), yellow (10-50), red (< 10)

**Panel 2 — Priority Ranking (Table)**
- All accounts sorted by score descending
- Columns: rank, account, score, session headroom, weekly headroom, next reset,
  status (burn/normal/exhausted/error)

**Panel 3 — Traffic Lights (Stat panel, repeated per account)**
- Green: headroom > 50% on both windows
- Yellow: headroom 20-50% OR in burn phase
- Red: headroom < 20% on binding window
- Grey: exhausted or errored

**Panel 4 — Score Over Time (Time series)**
- Each account's recommendation score over time
- Shows how priority shifts as accounts approach reset
- Useful for verifying the algorithm behaves correctly

### Implementation Approach

Recommendation scores are computed **in PromQL** using the existing metrics.
No new Go code is needed for the dashboard-only version:

```promql
# Effective headroom per account
min by (account) (
  100 - claude_usage_session_utilization_percent,
  100 - claude_usage_weekly_utilization_percent
)

# Burn phase detection
(claude_usage_session_reset_seconds - time()) < 1800
  and (100 - claude_usage_session_utilization_percent) > 0

# Simplified score (PromQL approximation)
clamp_min(
  (
    # Burn bonus
    ((claude_usage_session_reset_seconds - time() < 1800) * 1000)
    +
    # Effective headroom
    min by (account) (
      100 - claude_usage_session_utilization_percent,
      100 - claude_usage_weekly_utilization_percent
    )
  )
  *
  # Health penalty
  (1 - 0.5 * clamp_max(increase(claude_usage_poll_errors_total[5m]), 1))
, 0)
```

**Limitation**: PromQL's `min()` aggregation across different metrics is
awkward. A cleaner implementation computes the score in Go and exports it as a
new metric (see API section below).

## API & External Access

### New Prometheus Metric (Recommended)

Export the score as a new gauge from the Go exporter:

```
claude_usage_recommendation_score{account="...", org_id="..."}
```

This enables:
- Clean PromQL queries for dashboard panels
- External tooling to query Prometheus for the recommendation
- Alert rules based on recommendation score

### New HTTP Endpoint (Optional, for tooling integration)

```
GET /recommend
Response:
{
  "recommended": "personal",
  "score": 98.5,
  "reason": "highest effective headroom",
  "accounts": [
    {
      "name": "personal",
      "score": 98.5,
      "session_headroom": 88.2,
      "weekly_headroom": 62.1,
      "session_resets_in": "3h12m",
      "weekly_resets_in": "5d2h",
      "status": "normal"
    },
    ...
  ]
}
```

This endpoint allows shell scripts, Claude Code hooks, or other tools to
auto-select the best account:

```bash
# Example: auto-select account before starting a session
ACCOUNT=$(curl -s localhost:9091/recommend | jq -r .recommended)
```

### Configuration Changes

Add optional recommendation tuning to `accounts.yaml`:

```yaml
recommendation:
  burnThreshold: 30m    # Time before reset to enter burn phase
  healthWindow: 5m      # Window for poll error health check
  enabled: true         # Toggle recommendation engine
```

## Implementation Plan (Follow-up Beads)

### Phase 1: Score computation + metric export
- Add scoring logic to `poller.go` (runs per-poll, per-account)
- Export `claude_usage_recommendation_score` gauge
- Add `burnThreshold` and `healthWindow` to config
- Unit tests for scoring edge cases

### Phase 2: Dashboard panels
- Add "Use Now" stat panel
- Add priority ranking table
- Add traffic light indicators
- Add score time series

### Phase 3: HTTP recommendation endpoint
- Add `/recommend` handler to `main.go`
- Return JSON with full account ranking
- Integration test

### Phase 4: External tooling support
- Document how to query `/recommend` from scripts
- Example Claude Code hook for auto-account-selection
- Example shell alias

## Decision Log

| Decision | Rationale |
|----------|-----------|
| Score in Go, not PromQL | PromQL lacks cross-metric min(); Go is cleaner and testable |
| Burn threshold = 30m | Balances urgency with enough time to actually use the capacity |
| 0.5x health penalty (not exclude) | Unhealthy account may recover; total exclusion loses capacity |
| Effective headroom = min(session, weekly) | Automatically selects binding constraint |
| Freshness bonus = max 10 points | Small enough to never override headroom differences |
| `/recommend` endpoint optional | Dashboard covers most users; API is for power users |
