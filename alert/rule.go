package alert

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	LevelWarning  = 1
	LevelError    = 2
	LevelCritical = 3
)

type AlertRule struct {
	ID             int               `json:"id"`
	Topics         []string          `json:"topics"`
	Table          string            `json:"table"`
	Field          string            `json:"field"`
	Machine        string            `json:"machine"`
	Category       string            `json:"category"`
	Conditions     []AlertCondition  `json:"conditions"`
	LastAlertTime  map[int]time.Time `json:"-"` // Track last alert time for each device
	CooldownPeriod time.Duration     `json:"-"`
	mu             sync.Mutex        `json:"-"`
	logger         *zap.Logger
}

type AlertCondition struct {
	ID              int    `json:"id"`
	Device          string `json:"device"`
	Operator        string `json:"operator"`
	Threshold       int    `json:"threshold"`
	MessageTemplate string `json:"message_template"`
	Level           int    `json:"level"` // 1=Warning, 2=Error, 3=Critical
}

type AlertMessage struct {
	Device    string  `json:"device"`
	Current   float64 `json:"current"`
	Threshold float64 `json:"threshold"`
	Message   string  `json:"message"`
	Severity  string
}

// NewAlertRule is used to create a new AlertRule with the given parameters.
func NewAlertRule(id int, topics []string, table, field, category, machine string, conditions []AlertCondition, logger *zap.Logger) *AlertRule {
	return &AlertRule{
		ID:             id,
		Topics:         topics,
		Table:          table,
		Field:          field,
		Category:       category,
		Conditions:     conditions,
		Machine:        machine,
		LastAlertTime:  make(map[int]time.Time), // Ensure map initialization here
		CooldownPeriod: 30 * time.Second,        // For immediate issues
		logger:         logger,
	}
}

// Evaluate processes the payload and triggers an alert if conditions are met
func (r *AlertRule) Evaluate(payload map[string]any, condition AlertCondition) (bool, string) {
	// Convert payload values to float64 for consistent comparison
	floatPayload, err := r.convertPayload(payload)
	if err != nil {
		r.logger.Warn("Failed to convert payload", zap.Error(err))
		return false, ""
	}

	// Evaluate conditions with the converted payload
	triggered, _ := r.evaluateConditions(floatPayload)
	if !triggered {
		return false, ""
	}

	// Check if we should alert based on cooldown period
	if !r.shouldAlert(condition.ID) {
		return false, ""
	}

	// Format the alert message
	message := r.generateAlertMessage(condition, floatPayload[condition.Device])
	return true, message
}

func (r *AlertRule) convertPayload(payload map[string]any) (map[string]float64, error) {
	floatPayload := make(map[string]float64)
	for k, v := range payload {
		switch val := v.(type) {
		case float64:
			floatPayload[k] = val
		case float32:
			floatPayload[k] = float64(val)
		case int:
			floatPayload[k] = float64(val)
		case int32:
			floatPayload[k] = float64(val)
		case int64:
			floatPayload[k] = float64(val)
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				floatPayload[k] = f
			} else {
				return nil, fmt.Errorf("could not convert string value %v to float for device %s", val, k)
			}
		default:
			return nil, fmt.Errorf("unsupported value type %T for device %s", v, k)
		}
	}
	return floatPayload, nil
}

// evaluateConditions checks the payload against the conditions and returns whether any condition is triggered.
func (r *AlertRule) evaluateConditions(deviceValues map[string]float64) (bool, string) {
	for _, condition := range r.Conditions {
		// Check if condition contains complex logic (AND/OR)
		if strings.Contains(condition.Operator, "AND") || strings.Contains(condition.Operator, "OR") {
			if r.evaluateComplexCondition(condition.Operator, deviceValues) {
				return true, condition.Device
			}
		} else {
			// Simple condition
			if r.checkSimpleCondition(condition, deviceValues) {
				return true, condition.Device
			}
		}
	}
	return false, ""
}

// evaluateComplexCondition checks complex conditions with AND/OR logic
func (r *AlertRule) evaluateComplexCondition(operator string, values map[string]float64) bool {
	// Split into individual conditions
	var conditions []string
	if strings.Contains(operator, "AND") {
		conditions = strings.Split(operator, "AND")
	} else {
		conditions = strings.Split(operator, "OR")
	}

	// Trim spaces from each condition
	for i := range conditions {
		conditions[i] = strings.TrimSpace(conditions[i])
	}

	// Evaluate based on AND or OR logic
	if strings.Contains(operator, "AND") {
		return r.evaluateANDConditions(conditions, values)
	}
	return r.evaluateORConditions(conditions, values)
}

// evaluateANDConditions evaluates all conditions with AND logic
func (r *AlertRule) evaluateANDConditions(conditions []string, values map[string]float64) bool {
	for _, cond := range conditions {
		if !r.evaluateSingleCondition(cond, values) {
			return false
		}

	}
	return true

}

// evaluateORConditions evaluates conditions with OR logic
func (r *AlertRule) evaluateORConditions(conditions []string, values map[string]float64) bool {
	for _, cond := range conditions {
		if r.evaluateSingleCondition(cond, values) {
			return true
		}
	}
	return false
}

// evaluateSingleCondition evaluates a single condition (e.g., "D800 < 900")
func (r *AlertRule) evaluateSingleCondition(condition string, values map[string]float64) bool {
	// Parse the condition into parts
	parts := strings.Fields(condition)
	if len(parts) != 3 {
		r.logger.Warn("Invalid condition format", zap.String("condition", condition))
		return false
	}

	device := parts[0]
	operator := parts[1]
	thresholdStr := parts[2]

	// Get device value
	val, exists := values[device]
	if !exists {
		r.logger.Warn("Device not found in payload", zap.String("device", device))
		return false
	}

	// Get threshold value (either number or reference to another device)
	threshold, err := strconv.ParseFloat(thresholdStr, 64)
	if err != nil {
		// Try to get value from another device
		if refVal, exists := values[thresholdStr]; exists {
			threshold = refVal
		} else {
			r.logger.Warn("Invalid threshold in condition", zap.String("condition", condition))
			return false
		}
	}

	// Perform the comparison
	switch operator {
	case ">":
		return val > threshold
	case "<":
		return val < threshold
	case ">=":
		return val >= threshold
	case "<=":
		return val <= threshold
	case "==":
		return val == threshold
	case "!=":
		return val != threshold
	default:
		r.logger.Warn("Unsupported operator", zap.String("operator", operator))
		return false
	}
}

// checkCondition evaluates a simple condition based on the operator and threshold
func (r *AlertRule) checkSimpleCondition(condition AlertCondition, values map[string]float64) bool {
	val, exists := values[condition.Device]
	if !exists {
		return false
	}
	threshold := float64(condition.Threshold)

	switch condition.Operator {
	case ">":
		return val > threshold
	case "<":
		return val < threshold
	case ">=":
		return val >= threshold
	case "<=":
		return val <= threshold
	case "==":
		return val == threshold
	case "!=":
		return val != threshold
	default:
		r.logger.Warn("Unsupported operator", zap.String("operator", condition.Operator))
		return false
	}
}

// shouldAlert checks if we should trigger an alert based on cooldown period
func (r *AlertRule) shouldAlert(id int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.LastAlertTime == nil {
		r.LastAlertTime = make(map[int]time.Time)
	}

	now := time.Now()
	lastAlert, exists := r.LastAlertTime[id]

	if !exists || now.Sub(lastAlert) >= r.CooldownPeriod {
		r.LastAlertTime[id] = now
		return true
	}

	return false
}

// generateAlertMessage creates the formatted alert message
func (r *AlertRule) generateAlertMessage(condition AlertCondition, value float64) string {
	alert := AlertMessage{
		Device:    condition.Device,
		Current:   math.Round(value),
		Threshold: math.Round(float64(condition.Threshold)),
		Message:   condition.MessageTemplate,
		Severity:  getLevelString(condition.Level),
	}

	jsonBytes, err := json.Marshal(alert)
	if err != nil {
		r.logger.Warn("Failed to marshal alert message", zap.Error(err))
		return "{}"
	}

	// Add severity prefix if not already present
	// if !strings.HasPrefix(message, getLevelString(condition.Level)) {
	// 	message = fmt.Sprintf("%s: %s", getLevelString(condition.Level), message)
	// }

	// Log for debugging
	//fmt.Println("Generated message:", strings.ReplaceAll(msg, "{{value}}", fmt.Sprintf("%.2f", val)))

	return string(jsonBytes)
}
