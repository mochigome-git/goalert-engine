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
	ID             string            `json:"id"`
	TenantID       string            `json:"tenant_id"` // filters messages to correct tenant
	DeviceID       string            `json:"device_id"` // UUID — matches MQTT payload device_id
	Topics         []string          `json:"topics"`
	Table          string            `json:"table"`
	Field          string            `json:"field"`
	Device         string            `json:"device"` // label/name (was Machine)
	Category       string            `json:"category"`
	Conditions     []AlertCondition  `json:"conditions"`
	LastAlertTime  map[int]time.Time `json:"-"`
	CooldownPeriod time.Duration     `json:"-"`
	mu             sync.Mutex        `json:"-"`
	logger         *zap.Logger
}

type AlertCondition struct {
	ID              int             `json:"id"`
	Device          string          `json:"device"`
	FieldName       string          `json:"fieldName"`
	Operator        string          `json:"operator"`
	Threshold       json.RawMessage `json:"threshold"` // string or number in DB
	Unit            []string        `json:"unit"`
	MessageTemplate string          `json:"message_template"`
	Level           int             `json:"level"`
}

// ThresholdFloat parses the threshold regardless of whether it was stored
// as a number (200) or a string ("200") in the jsonb conditions column.
func (c *AlertCondition) ThresholdFloat() float64 {
	if len(c.Threshold) == 0 {
		return 0
	}
	// Try number first
	var f float64
	if err := json.Unmarshal(c.Threshold, &f); err == nil {
		return f
	}
	// Try quoted string
	var s string
	if err := json.Unmarshal(c.Threshold, &s); err == nil {
		var f2 float64
		if _, err := fmt.Sscanf(s, "%f", &f2); err == nil {
			return f2
		}
	}
	return 0
}

type AlertMessage struct {
	Device    string   `json:"device"`
	Current   float64  `json:"current"`
	Threshold float64  `json:"threshold"`
	Message   string   `json:"message"`
	Unit      []string `json:"unit"`
	Severity  string   `json:"severity"`
}

func NewAlertRule(id, tenantID, deviceID string, topics []string, table, field, category, device string, conditions []AlertCondition, logger *zap.Logger) *AlertRule {
	return &AlertRule{
		ID:             id,
		TenantID:       tenantID,
		DeviceID:       deviceID,
		Topics:         topics,
		Table:          table,
		Field:          field,
		Category:       category,
		Device:         device,
		Conditions:     conditions,
		LastAlertTime:  make(map[int]time.Time),
		CooldownPeriod: 30 * time.Second,
		logger:         logger,
	}
}

// Evaluate checks cached device data against one condition.
// data keys are lowercase field names from the MQTT payload data{} object.
func (r *AlertRule) Evaluate(data map[string]float64, condition AlertCondition) (bool, string) {
	if len(data) == 0 {
		return false, ""
	}

	if !r.evaluateExpression(condition.Operator, data) {
		return false, ""
	}

	if !r.shouldAlert(condition.ID) {
		return false, ""
	}

	currentVal := data[strings.ToLower(condition.FieldName)]
	return true, r.generateAlertMessage(condition, currentVal)
}

// evaluateExpression handles simple and AND/OR compound expressions.
// Format: "fieldname operator threshold [AND|OR fieldname operator threshold]"
func (r *AlertRule) evaluateExpression(expr string, data map[string]float64) bool {
	upper := strings.ToUpper(expr)

	if strings.Contains(upper, " AND ") {
		for _, p := range splitOn(expr, "AND") {
			if !r.evalSingle(strings.TrimSpace(p), data) {
				return false
			}
		}
		return true
	}

	if strings.Contains(upper, " OR ") {
		for _, p := range splitOn(expr, "OR") {
			if r.evalSingle(strings.TrimSpace(p), data) {
				return true
			}
		}
		return false
	}

	return r.evalSingle(strings.TrimSpace(expr), data)
}

// evalSingle evaluates "fieldname operator threshold", case-insensitive field lookup.
func (r *AlertRule) evalSingle(expr string, data map[string]float64) bool {
	parts := strings.Fields(expr)
	if len(parts) != 3 {
		r.logger.Warn("Invalid condition expression", zap.String("expr", expr))
		return false
	}

	fieldName := strings.ToLower(parts[0])
	operator := parts[1]
	threshStr := parts[2]

	val, exists := data[fieldName]
	if !exists {
		r.logger.Warn("Field not found in device data",
			zap.String("field", fieldName),
			zap.Strings("available", keysOf(data)),
		)
		return false
	}

	threshold, err := strconv.ParseFloat(threshStr, 64)
	if err != nil {
		if ref, ok := data[strings.ToLower(threshStr)]; ok {
			threshold = ref
		} else {
			r.logger.Warn("Invalid threshold", zap.String("expr", expr))
			return false
		}
	}

	switch operator {
	case ">":
		return val > threshold
	case ">=":
		return val >= threshold
	case "<":
		return val < threshold
	case "<=":
		return val <= threshold
	case "==":
		return val == threshold
	case "!=":
		return val != threshold
	default:
		r.logger.Warn("Unsupported operator", zap.String("op", operator))
		return false
	}
}

func splitOn(expr, logicalOp string) []string {
	sep := " " + logicalOp + " "
	upper := strings.ToUpper(expr)
	var parts []string
	for {
		idx := strings.Index(strings.ToUpper(upper), strings.ToUpper(sep))
		if idx < 0 {
			parts = append(parts, expr)
			break
		}
		parts = append(parts, expr[:idx])
		expr = expr[idx+len(sep):]
		upper = strings.ToUpper(expr)
	}
	return parts
}

func keysOf(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (r *AlertRule) shouldAlert(id int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.LastAlertTime == nil {
		r.LastAlertTime = make(map[int]time.Time)
	}
	now := time.Now()
	last, exists := r.LastAlertTime[id]
	if !exists || now.Sub(last) >= r.CooldownPeriod {
		r.LastAlertTime[id] = now
		return true
	}
	return false
}

func (r *AlertRule) generateAlertMessage(condition AlertCondition, currentValue float64) string {
	unit := ""
	if len(condition.Unit) > 0 {
		unit = condition.Unit[0]
	}

	// Render the message template with actual values
	rendered := condition.MessageTemplate
	if rendered == "" {
		rendered = "{{device}} exceeded threshold — currently {{value}}{{unit}} (limit: {{threshold}}{{unit}})"
	}
	rendered = strings.ReplaceAll(rendered, "{{device}}", condition.Device)
	rendered = strings.ReplaceAll(rendered, "{{field}}", strings.ToLower(condition.FieldName))
	rendered = strings.ReplaceAll(rendered, "{{value}}", fmt.Sprintf("%.2f", currentValue))
	rendered = strings.ReplaceAll(rendered, "{{threshold}}", fmt.Sprintf("%.2f", condition.ThresholdFloat()))
	rendered = strings.ReplaceAll(rendered, "{{unit}}", unit)
	rendered = strings.ReplaceAll(rendered, "{{timestamp}}", time.Now().Format("2006-01-02 15:04:05"))

	alert := AlertMessage{
		Device:    condition.Device,
		Current:   math.Round(currentValue*100) / 100,
		Threshold: condition.ThresholdFloat(),
		Message:   rendered,
		Unit:      condition.Unit,
		Severity:  getLevelString(condition.Level),
	}
	b, err := json.Marshal(alert)
	if err != nil {
		r.logger.Warn("Failed to marshal alert", zap.Error(err))
		return fmt.Sprintf(`{"error":"marshal failed","device":"%s"}`, condition.Device)
	}
	return string(b)
}
