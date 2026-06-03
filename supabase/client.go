package supabase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"goalert-engine/config"
	"io"
	"net/http"
	"time"
)

// SupabaseInserter implements alert.AlertInserter
type SupabaseInserter struct{}

func (s *SupabaseInserter) InsertAlert(cfg config.Config, table, device, message, category, deviceLabel, tenantID string) error {
	return InsertAlert(cfg, table, device, message, category, deviceLabel, tenantID)
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 100,
	},
}

func InsertAlert(cfg config.Config, table, device, message, category, deviceLabel, tenantID string) error {
	const alertSchema = "alerts"

	url := fmt.Sprintf("%s/rest/v1/%s", cfg.SupabaseURL, table)

	var messageJSON any
	if err := json.Unmarshal([]byte(message), &messageJSON); err != nil {
		messageJSON = map[string]string{"raw": message}
	}

	requestBody := map[string]any{
		"device_id": device,
		"message":   messageJSON,
		"category":  category,
		"device":    deviceLabel,
		"tenant_id": tenantID, // ← from rule.TenantID
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("apikey", cfg.SupabaseKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	req.Header.Set("Content-Profile", alertSchema)
	req.Header.Set("Accept-Profile", alertSchema)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("supabase insert error (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}
