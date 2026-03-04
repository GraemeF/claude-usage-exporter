package main

import (
	"context"
	"log"
	"math"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const burnThreshold = 30 * time.Minute

type pollerConfig struct {
	ActiveInterval   time.Duration
	IdleInterval     time.Duration
	IdleThreshold    int
	ResetBurstWindow time.Duration
}

type accountPoller struct {
	acc            Account
	cfg            pollerConfig
	prevSession    float64
	prevWeekly     float64
	unchangedCount int
	interval       time.Duration
	mu             sync.Mutex

	recentErrors int // rolling error count for health penalty

	// instruments
	sessionUtil      metric.Float64Gauge
	weeklyUtil       metric.Float64Gauge
	sessionReset     metric.Float64Gauge
	weeklyReset      metric.Float64Gauge
	lastSuccess      metric.Float64Gauge
	pollInterval     metric.Float64Gauge
	pollErrors       metric.Int64Counter
	recommendScore   metric.Float64Gauge
}

func newAccountPoller(acc Account, cfg pollerConfig, meter metric.Meter) (*accountPoller, error) {
	sessionUtil, err := meter.Float64Gauge("claude.usage.session.utilization",
		metric.WithDescription("Claude.ai 5-hour session window utilization (0–100)"),
		metric.WithUnit("%"))
	if err != nil {
		return nil, err
	}

	weeklyUtil, err := meter.Float64Gauge("claude.usage.weekly.utilization",
		metric.WithDescription("Claude.ai 7-day weekly utilization (0–100)"),
		metric.WithUnit("%"))
	if err != nil {
		return nil, err
	}

	sessionReset, err := meter.Float64Gauge("claude.usage.session.reset",
		metric.WithDescription("Unix timestamp when the 5-hour session window resets"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}

	weeklyReset, err := meter.Float64Gauge("claude.usage.weekly.reset",
		metric.WithDescription("Unix timestamp when the 7-day weekly limit resets"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}

	lastSuccess, err := meter.Float64Gauge("claude.usage.poll.last_success",
		metric.WithDescription("Unix timestamp of the last successful poll"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}

	pollInterval, err := meter.Float64Gauge("claude.usage.poll.interval",
		metric.WithDescription("Current adaptive poll interval in seconds"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}

	pollErrors, err := meter.Int64Counter("claude.usage.poll.errors",
		metric.WithDescription("Total number of failed polls per account"))
	if err != nil {
		return nil, err
	}

	recommendScore, err := meter.Float64Gauge("claude.usage.recommendation.score",
		metric.WithDescription("Account recommendation score: higher means prefer this account"))
	if err != nil {
		return nil, err
	}

	return &accountPoller{
		acc:          acc,
		cfg:          cfg,
		interval:     cfg.ActiveInterval,
		sessionUtil:  sessionUtil,
		weeklyUtil:   weeklyUtil,
		sessionReset: sessionReset,
		weeklyReset:  weeklyReset,
		lastSuccess:  lastSuccess,
		pollInterval:   pollInterval,
		pollErrors:     pollErrors,
		recommendScore: recommendScore,
	}, nil
}

func (p *accountPoller) run() {
	p.doPoll()
	for {
		p.mu.Lock()
		interval := p.interval
		p.mu.Unlock()
		time.Sleep(interval)
		p.doPoll()
	}
}

func (p *accountPoller) doPoll() {
	ctx := context.Background()
	attrs := metric.WithAttributes(
		attribute.String("account", p.acc.Name),
		attribute.String("org_id", p.acc.OrgID),
	)

	usage, err := fetchUsage(p.acc)
	if err != nil {
		log.Printf("[%s] poll error: %v", p.acc.Name, err)
		p.pollErrors.Add(ctx, 1, attrs)
		p.mu.Lock()
		p.recentErrors++
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.recentErrors = 0

	var sessionU, weeklyU float64
	var timeToSessionReset, timeToWeeklyReset time.Duration
	now := time.Now()
	burstNeeded := false

	if w := usage.FiveHour; w != nil {
		sessionU = clamp(w.Utilization)
		p.sessionUtil.Record(ctx, sessionU, attrs)
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
			p.sessionReset.Record(ctx, float64(t.Unix()), attrs)
			timeToSessionReset = t.Sub(now)
			if d := timeToSessionReset; d > 0 && d < p.cfg.ResetBurstWindow {
				burstNeeded = true
			}
		}
	}

	if w := usage.SevenDay; w != nil {
		weeklyU = clamp(w.Utilization)
		p.weeklyUtil.Record(ctx, weeklyU, attrs)
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
			p.weeklyReset.Record(ctx, float64(t.Unix()), attrs)
			timeToWeeklyReset = t.Sub(now)
			if d := timeToWeeklyReset; d > 0 && d < p.cfg.ResetBurstWindow {
				burstNeeded = true
			}
		}
	}

	p.lastSuccess.Record(ctx, float64(now.Unix()), attrs)

	// Adaptive interval: back off when idle, snap back when active or near reset.
	changed := sessionU != p.prevSession || weeklyU != p.prevWeekly
	p.prevSession = sessionU
	p.prevWeekly = weeklyU

	switch {
	case burstNeeded || changed:
		p.unchangedCount = 0
		p.interval = p.cfg.ActiveInterval
	default:
		p.unchangedCount++
		if p.unchangedCount >= p.cfg.IdleThreshold {
			doubled := p.interval * 2
			if doubled > p.cfg.IdleInterval {
				doubled = p.cfg.IdleInterval
			}
			p.interval = doubled
		}
	}

	p.pollInterval.Record(ctx, p.interval.Seconds(), attrs)

	// Recommendation score computation.
	score := computeRecommendationScore(sessionU, weeklyU, timeToSessionReset, timeToWeeklyReset, p.recentErrors)
	p.recommendScore.Record(ctx, score, attrs)

	log.Printf("[%s] session=%.1f%% weekly=%.1f%% score=%.1f next=%s",
		p.acc.Name, sessionU, weeklyU, score, p.interval)
}

// computeRecommendationScore returns a priority score for an account.
// Higher score = prefer this account. Burn-phase accounts (near reset with
// remaining headroom) get score > 1000 to ensure they're used before the
// window resets. Exhausted accounts get -1.
func computeRecommendationScore(sessionU, weeklyU float64, timeToSession, timeToWeekly time.Duration, recentErrors int) float64 {
	sessionHeadroom := 100 - sessionU
	weeklyHeadroom := 100 - weeklyU
	effectiveHeadroom := math.Min(sessionHeadroom, weeklyHeadroom)

	if effectiveHeadroom <= 0 {
		return -1 // exhausted
	}

	var score float64

	// Burn phase: account resets soon and still has headroom — prioritise burning it.
	switch {
	case timeToSession > 0 && timeToSession < burnThreshold && sessionHeadroom > 0:
		score = 1000 + sessionHeadroom
	case timeToWeekly > 0 && timeToWeekly < burnThreshold && weeklyHeadroom > 0:
		score = 1000 + weeklyHeadroom
	default:
		score = effectiveHeadroom
		// Freshness bonus: mild tie-breaker favouring accounts with more time until reset.
		// Range 0–10 points. 18000s = 5 hours (session window).
		if timeToSession > 0 {
			score += math.Min(timeToSession.Seconds()/18000, 1.0) * 10
		}
	}

	// Health penalty: halve score if recent poll errors.
	if recentErrors > 0 {
		score *= 0.5
	}

	return score
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
