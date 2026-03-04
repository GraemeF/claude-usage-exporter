package main

import (
	"testing"
	"time"
)

func TestComputeRecommendationScore(t *testing.T) {
	tests := []struct {
		name           string
		sessionU       float64
		weeklyU        float64
		timeToSession  time.Duration
		timeToWeekly   time.Duration
		recentErrors   int
		wantMin        float64
		wantMax        float64
		wantExact      *float64
	}{
		{
			name:      "exhausted session",
			sessionU:  100, weeklyU: 50,
			timeToSession: 2 * time.Hour, timeToWeekly: 24 * time.Hour,
			wantExact: ptr(-1),
		},
		{
			name:      "exhausted weekly",
			sessionU:  50, weeklyU: 100,
			timeToSession: 2 * time.Hour, timeToWeekly: 24 * time.Hour,
			wantExact: ptr(-1),
		},
		{
			name:      "burn phase session - 20min to reset, 40% used",
			sessionU:  40, weeklyU: 10,
			timeToSession: 20 * time.Minute, timeToWeekly: 3 * 24 * time.Hour,
			wantMin:   1060, wantMax: 1060, // 1000 + 60 session headroom
		},
		{
			name:      "burn phase weekly - 20min to reset, 50% used",
			sessionU:  10, weeklyU: 50,
			timeToSession: 4 * time.Hour, timeToWeekly: 20 * time.Minute,
			wantMin:   1050, wantMax: 1050, // 1000 + 50 weekly headroom
		},
		{
			name:      "normal - 10% used, lots of time",
			sessionU:  10, weeklyU: 10,
			timeToSession: 4 * time.Hour, timeToWeekly: 5 * 24 * time.Hour,
			wantMin:   90, wantMax: 100, // ~90 headroom + freshness bonus up to 10
		},
		{
			name:      "health penalty halves score",
			sessionU:  10, weeklyU: 10,
			timeToSession: 4 * time.Hour, timeToWeekly: 5 * 24 * time.Hour,
			recentErrors: 1,
			wantMin:   45, wantMax: 50, // (90 + bonus) * 0.5
		},
		{
			name:      "burn phase with health penalty",
			sessionU:  40, weeklyU: 10,
			timeToSession: 20 * time.Minute, timeToWeekly: 3 * 24 * time.Hour,
			recentErrors: 2,
			wantMin:   530, wantMax: 530, // (1000 + 60) * 0.5
		},
		{
			name:      "nearly exhausted burn phase",
			sessionU:  95, weeklyU: 10,
			timeToSession: 20 * time.Minute, timeToWeekly: 3 * 24 * time.Hour,
			wantMin:   1005, wantMax: 1005, // 1000 + 5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeRecommendationScore(tt.sessionU, tt.weeklyU, tt.timeToSession, tt.timeToWeekly, tt.recentErrors)

			if tt.wantExact != nil {
				if got != *tt.wantExact {
					t.Errorf("got %f, want exactly %f", got, *tt.wantExact)
				}
				return
			}
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("got %f, want in [%f, %f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func ptr(f float64) *float64 { return &f }
