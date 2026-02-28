package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const claudeBaseURL = "https://claude.ai"

type usageWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type usageResponse struct {
	FiveHour *usageWindow `json:"five_hour"`
	SevenDay *usageWindow `json:"seven_day"`
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func fetchUsage(acc Account) (*usageResponse, error) {
	url := fmt.Sprintf("%s/api/organizations/%s/usage", claudeBaseURL, acc.OrgID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "sessionKey="+acc.SessionKey)
	req.Header.Set("User-Agent", "claude-usage-exporter/0.1")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var usage usageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &usage, nil
}
