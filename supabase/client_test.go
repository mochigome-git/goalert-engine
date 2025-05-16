package supabase

import (
	"bytes"
	"encoding/json"
	"errors"
	"goalert-engine/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInsertAlert(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name           string
		mockResponse   *http.Response
		mockError      error
		expectedError  string
		expectedMethod string
		expectedURL    string
		expectedBody   map[string]interface{}
	}{
		{
			name: "successful insert",
			mockResponse: &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
			},
			expectedMethod: "POST",
			expectedURL:    "/rest/v1/alerts",
			expectedBody: map[string]interface{}{
				"device_id": "device123",
				"message":   "test message",
			},
		},
		{
			name: "api error response",
			mockResponse: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":"invalid request"}`)),
			},
			expectedError: "API error (400): {\"error\":\"invalid request\"}",
		},
		{
			name:          "http client error",
			mockError:     errors.New("connection failed"),
			expectedError: "connection failed", // Just check for this substring
		},
	}

	// Backup and restore the original client
	originalClient := httpClient
	defer func() { httpClient = originalClient }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock server if needed
			if tt.mockResponse != nil || tt.mockError != nil {
				httpClient = &http.Client{
					Transport: &mockTransport{
						response: tt.mockResponse,
						err:      tt.mockError,
					},
				}
			}

			// Create test config
			cfg := config.Config{
				SupabaseURL: "http://example.com",
				SupabaseKey: "test-key",
				Schema:      "public",
			}

			// Call the function
			err := InsertAlert(cfg, "alerts", "device123", "test message")

			// Check errors
			if tt.expectedError != "" {
				if err == nil {
					t.Errorf("expected error containing '%s', got nil", tt.expectedError)
					return
				}

				if !strings.Contains(err.Error(), tt.expectedError) {
					t.Errorf("expected error to contain '%s', got '%v'", tt.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestInsertAlertWithMockServer(t *testing.T) {
	// Setup test cases
	tests := []struct {
		name           string
		statusCode     int
		responseBody   string
		expectedError  string
		expectedMethod string
		expectedBody   map[string]interface{}
	}{
		{
			name:           "successful insert with mock server",
			statusCode:     http.StatusCreated,
			responseBody:   `{}`,
			expectedMethod: "POST",
			expectedBody: map[string]interface{}{
				"device_id": "device123",
				"message":   "test message",
			},
		},
		{
			name:           "validation error",
			statusCode:     http.StatusBadRequest,
			responseBody:   `{"error":"validation failed"}`,
			expectedError:  "API error (400): {\"error\":\"validation failed\"}",
			expectedMethod: "POST",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify method
				if r.Method != tt.expectedMethod {
					t.Errorf("expected method %s, got %s", tt.expectedMethod, r.Method)
				}

				// Verify headers
				if r.Header.Get("apikey") != "test-key" {
					t.Error("missing or incorrect apikey header")
				}
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("missing or incorrect authorization header")
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Error("missing or incorrect content-type header")
				}

				// Verify body if expected
				if tt.expectedBody != nil {
					var body map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("failed to decode request body: %v", err)
					}

					for k, v := range tt.expectedBody {
						if body[k] != v {
							t.Errorf("expected body field %s to be %v, got %v", k, v, body[k])
						}
					}
				}

				// Send response
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			// Create test config with mock server URL
			cfg := config.Config{
				SupabaseURL: server.URL,
				SupabaseKey: "test-key",
				Schema:      "public",
			}

			// Call the function
			err := InsertAlert(cfg, "alerts", "device123", "test message")

			// Check errors
			if tt.expectedError != "" {
				if err == nil || err.Error() != tt.expectedError {
					t.Errorf("expected error '%s', got '%v'", tt.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// mockTransport implements http.RoundTripper for testing
type mockTransport struct {
	response *http.Response
	err      error
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.response, m.err
}
