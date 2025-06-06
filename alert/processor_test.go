package alert

import (
	"context"
	"math"
	"testing"
	"time"

	"goalert-engine/config"
	"goalert-engine/supabase"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestNewRuleManager(t *testing.T) {
	rules := []AlertRule{
		{
			ID:             "3d5df7e3-5ac8-42b8-ae79-4a54cf7e90e7",
			Topics:         []string{"topic1"},
			Table:          "alerts",
			CooldownPeriod: 0, // Should be set automatically
			Conditions: []AlertCondition{
				{
					Device:    "device1",
					Level:     LevelWarning,
					Operator:  ">",
					Threshold: 10,
				},
			},
		},
	}

	cfg := config.Config{}
	inserter := &supabase.SupabaseInserter{}
	rm := NewRuleManager(context.Background(), rules, cfg, inserter, nil)

	if len(rm.ruleChans) != 1 {
		t.Errorf("Expected 1 rule channel, got %d", len(rm.ruleChans))
	}

	if rm.Rules[0].CooldownPeriod == 0 {
		t.Error("Expected cooldown period to be set automatically")
	}
}

func TestHandleMQTTMessage(t *testing.T) {
	logger := zap.NewNop()
	rules := []AlertRule{
		{
			ID:     "3d5df7e3-5ac8-42b8-ae79-4a54cf7e90e7",
			Topics: []string{"sensor/device1"},
			Table:  "alerts",
			Conditions: []AlertCondition{
				{
					Device:    "device1",
					Level:     LevelWarning,
					Operator:  ">",
					Threshold: 10,
				},
			},
		},
	}

	cfg := config.Config{}
	inserter := &supabase.SupabaseInserter{}
	rm := NewRuleManager(context.Background(), rules, cfg, inserter, logger)

	// Test valid message
	payload := `{"address": "device1", "value": 15}`
	rm.HandleMQTTMessage("sensor/device1", []byte(payload), cfg)

	key := cacheKey{
		Topic:   "sensor/device1",
		Address: "device1",
	}

	rm.mu.RLock()
	cached, exists := rm.deviceCache[key]
	rm.mu.RUnlock()

	if !exists {
		t.Error("Expected device1 to be in cache")
	}

	if cached.value.(float64) != 15 {
		t.Errorf("Expected cached value to be 15, got %v", cached.value)
	}

	// Test invalid message (missing address)
	invalidPayload := `{"value": 15}`
	rm.HandleMQTTMessage("sensor/device1", []byte(invalidPayload), cfg)
}

// MockSupabaseClient implements the AlertInserter interface for testing
type MockSupabaseClient struct {
	InsertAlertFunc func(cfg config.Config, table, device, message, category, machine string) error
}

func (m *MockSupabaseClient) InsertAlert(cfg config.Config, table, device, message, category, machine string) error {
	return m.InsertAlertFunc(cfg, table, device, message, category, machine)
}

func TestEvaluateRule(t *testing.T) {
	// Create our mock client
	mockClient := &MockSupabaseClient{
		InsertAlertFunc: func(cfg config.Config, table, device, message, category, machine string) error {
			if table != "alerts" {
				t.Errorf("Expected table 'alerts', got '%s'", table)
			}
			if device != "device2" {
				t.Errorf("Expected device 'device2', got '%s'", device)
			}
			if message == "" {
				t.Error("Expected non-empty message")
			}
			if category == "" {
				t.Error("Expected non-empty category")
			}
			if machine == "" {
				t.Error("Expected non-empty machine")
			}
			return nil
		},
	}

	logger := zaptest.NewLogger(t)

	rules := []AlertRule{
		{
			ID:     "3d5df7e3-5ac8-42b8-ae79-4a54cf7e90e7",
			logger: logger,
			Topics: []string{"sensor/device1", "sensor/device2"},
			Table:  "alerts",
			Conditions: []AlertCondition{
				{
					Device:    "device1",
					Level:     LevelWarning,
					Operator:  ">",
					Threshold: 10,
				},
				{
					Device:    "device2",
					Level:     LevelCritical,
					Operator:  "<",
					Threshold: 5,
				},
			},
		},
	}

	key := cacheKey{
		Topic:   "sensor/device1",
		Address: "device1",
	}

	key2 := cacheKey{
		Topic:   "sensor/device2",
		Address: "device2",
	}

	cfg := config.Config{}
	rm := NewRuleManager(context.Background(), rules, cfg, mockClient, logger)

	// Prime the cache with values
	rm.mu.Lock()
	rm.deviceCache[key] = cachedValue{value: 15, timestamp: time.Now()}
	rm.deviceCache[key2] = cachedValue{value: 3, timestamp: time.Now()}
	rm.mu.Unlock()

	rm.evaluateRule(&rules[0], cfg)
}
func TestShouldTriggerAlert(t *testing.T) {
	rm := &RuleManager{
		lastAlertTimes: make(map[string]time.Time),
		alertCounts:    make(map[string]int),
	}

	alertKey := "1_2" // rule 1, level 2 (Error)

	// First alert should always trigger
	if !rm.shouldTriggerAlert(alertKey, LevelError) {
		t.Error("First alert should trigger")
	}

	// Mark alert as triggered
	rm.markAlertTriggered(alertKey, LevelError)

	// Immediate retry should not trigger (in cooldown)
	if rm.shouldTriggerAlert(alertKey, LevelError) {
		t.Error("Alert should be in cooldown")
	}

	// Wait longer than base cooldown (1 minute for Error)
	rm.lastAlertTimes[alertKey] = time.Now().Add(-2 * time.Minute)
	if !rm.shouldTriggerAlert(alertKey, LevelError) {
		t.Error("Alert should trigger after cooldown")
	}
}

func TestAlertCooldownBackoff(t *testing.T) {
	rm := &RuleManager{
		lastAlertTimes: make(map[string]time.Time),
		alertCounts:    make(map[string]int),
	}

	alertKey := "1_2" // rule 1, level 2 (Error)
	baseCooldown := rm.getBaseCooldown(LevelError)

	// Trigger alerts multiple times
	for i := 0; i < 3; i++ {
		rm.markAlertTriggered(alertKey, LevelError)
	}

	// Cooldown should increase with each alert
	cooldown := rm.getCooldown(alertKey, LevelError)
	expected := time.Duration(float64(baseCooldown) * math.Pow(2, 3))
	if cooldown != expected {
		t.Errorf("Expected cooldown %v, got %v", expected, cooldown)
	}

	// Test max cooldown
	for i := 0; i < 10; i++ {
		rm.markAlertTriggered(alertKey, LevelError)
	}
	cooldown = rm.getCooldown(alertKey, LevelError)
	maxCooldown := baseCooldown * 8
	if cooldown > maxCooldown {
		t.Errorf("Cooldown %v exceeds max %v", cooldown, maxCooldown)
	}
}

func TestCreateRuleSnapshot(t *testing.T) {
	rules := []AlertRule{
		{
			ID:     "3d5df7e3-5ac8-42b8-ae79-4a54cf7e90e7",
			Topics: []string{"sensor/device1", "sensor/device2"},
			Conditions: []AlertCondition{
				{
					Device: "device1",
				},
				{
					Device: "device2",
				},
			},
		},
	}

	key := cacheKey{
		Topic:   "sensor/device1",
		Address: "device1",
	}

	key2 := cacheKey{
		Topic:   "sensor/device2",
		Address: "device2",
	}

	inserter := &supabase.SupabaseInserter{}

	rm := NewRuleManager(context.Background(), rules, config.Config{}, inserter, nil)
	now := time.Now()

	// Add fresh values to cache
	rm.mu.Lock()
	rm.deviceCache[key] = cachedValue{value: 10, timestamp: now}
	rm.deviceCache[key2] = cachedValue{value: 20, timestamp: now}
	rm.mu.Unlock()

	snapshot := rm.createRuleSnapshot(&rules[0])
	if snapshot == nil {
		t.Fatal("Expected non-nil snapshot")
	}

	if len(snapshot) != 2 {
		t.Errorf("Expected snapshot with 2 values, got %d", len(snapshot))
	}

	// Test with expired cache
	rm.mu.Lock()
	rm.deviceCache[key] = cachedValue{value: 10, timestamp: now.Add(-10 * time.Minute)}
	rm.mu.Unlock()

	snapshot = rm.createRuleSnapshot(&rules[0])
	if snapshot != nil {
		t.Error("Expected nil snapshot due to expired cache")
	}
}

func TestIsValidValue(t *testing.T) {
	tests := []struct {
		value any
		valid bool
	}{
		{15.0, true},
		{0.0, false},
		{"", false},
		{"value", true},
		{nil, false},
		{5, true},
		{0, false},
	}

	for _, tt := range tests {
		if isValidValue(tt.value) != tt.valid {
			t.Errorf("isValidValue(%v) should be %v", tt.value, tt.valid)
		}
	}
}
