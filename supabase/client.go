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

func (s *SupabaseInserter) InsertAlert(cfg config.Config, table, device, message, category, deviceLabel string) error {
	return InsertAlert(cfg, table, device, message, category, deviceLabel)
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 100,
	},
}

// InsertAlert writes one row to alerts.logs.
//
// alerts.logs schema:
//
//	uuid        uuid  (auto)
//	created_at  timestamptz (auto)
//	device_id   text  — the field name that triggered (e.g. "energy_reactive_total_kvarh")
//	message     jsonb — parsed from the JSON alert message string
//	category    text  — rule.Category
//	device      text  — rule.Device (device label/name, was "machine")
func InsertAlert(cfg config.Config, table, device, message, category, deviceLabel string) error {
	// alerts schema is fixed — cfg.Schema is typically "public" and must not be used here
	const alertSchema = "alerts"

	url := fmt.Sprintf("%s/rest/v1/%s", cfg.SupabaseURL, table)

	// message is a JSON string — unmarshal so Supabase stores it as jsonb,
	// not as a double-encoded string like "\"{ \\\"device\\\": ... }\""
	var messageJSON any
	if err := json.Unmarshal([]byte(message), &messageJSON); err != nil {
		messageJSON = map[string]string{"raw": message}
	}

	requestBody := map[string]any{
		"device_id": device,      // field name / condition identifier
		"message":   messageJSON, // jsonb
		"category":  category,
		"device":    deviceLabel, // device label (column was named "device", not "machine")
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
