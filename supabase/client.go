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

// SupabaseInserter wraps the package-level InsertAlert function
// to implement the alert.AlertInserter interface
type SupabaseInserter struct{}

func (s *SupabaseInserter) InsertAlert(cfg config.Config, table, device, message string) error {
	return InsertAlert(cfg, table, device, message)
}

// Shared client with connection pooling
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
		MaxIdleConnsPerHost: 100,
	},
}

func InsertAlert(cfg config.Config, table string, deviceID string, message string) error {
	// Construct REST API endpoint URL
	url := fmt.Sprintf("%s/rest/v1/%s", cfg.SupabaseURL, table)

	// Prepare request body
	requestBody := map[string]any{
		"device_id": deviceID,
		"message":   message,
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set required headers
	req.Header.Set("apikey", cfg.SupabaseKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	// For private schemas (uncomment if needed)
	req.Header.Set("Content-Profile", cfg.Schema)
	req.Header.Set("Accept-Profile", cfg.Schema)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Always read the response body to allow connection reuse
	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}
