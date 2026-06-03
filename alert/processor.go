package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"goalert-engine/config"
	"goalert-engine/supabase"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MQTTPayload — actual shape from EMQX:
// Topic: telemetry/{tenant_id}/{device_id}/{category}
// Body:
//
//	{
//	  "tenant_id": "c61336db-...",
//	  "device_id": "c9f77eb6-...",
//	  "energy": { "p_active_kw": 6.037, "v_rms": 220.76, ... }
//	}
//
// The category key matches the last topic segment.
// We flatten all nested numeric values into one map for condition evaluation.
type MQTTPayload struct {
	TenantID string         `json:"tenant_id"`
	DeviceID string         `json:"device_id"`
	Kind     string         `json:"kind"`
	Extra    map[string]any `json:"-"` // all non-standard keys (category objects)
}

func (m *MQTTPayload) UnmarshalJSON(b []byte) error {
	type plain struct {
		TenantID string `json:"tenant_id"`
		DeviceID string `json:"device_id"`
		Kind     string `json:"kind"`
	}
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	m.TenantID = p.TenantID
	m.DeviceID = p.DeviceID
	m.Kind = p.Kind

	var all map[string]any
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	skip := map[string]bool{"tenant_id": true, "device_id": true, "kind": true}
	m.Extra = make(map[string]any)
	for k, v := range all {
		if !skip[k] {
			m.Extra[k] = v
		}
	}
	return nil
}

// FlatData flattens nested category objects into field → value.
// { "energy": { "p_active_kw": 6.037 } } → { "p_active_kw": 6.037 }
func (m *MQTTPayload) FlatData() map[string]any {
	out := make(map[string]any)
	for _, v := range m.Extra {
		switch val := v.(type) {
		case map[string]any:
			for fk, fv := range val {
				out[fk] = fv
			}
		default:
			// top-level numeric value — unlikely but handle it
		}
	}
	return out
}

// ----------------------------------------------------------------------

type cachedValue struct {
	value     map[string]float64
	timestamp time.Time
}

// cacheKey kept for backwards-compat with existing tests
type cacheKey struct {
	Topic   string
	Address string
}

type AlertInserter interface {
	InsertAlert(cfg config.Config, table, device, message, category, deviceLabel, tenantID string) error
}

type RuleManager struct {
	Rules          []AlertRule
	Cfg            config.Config
	ruleChans      map[string]chan struct{}
	deviceCache    map[string]cachedValue // key = device_id UUID
	mu             sync.RWMutex
	cacheTTL       time.Duration
	lastAlertTimes map[string]time.Time
	alertCounts    map[string]int
	alertMu        sync.Mutex
	alertInserter  AlertInserter
	ctx            context.Context
	cancel         context.CancelFunc
	logger         *zap.Logger
}

func NewRuleManager(ctx context.Context, rules []AlertRule, cfg config.Config, inserter AlertInserter, logger *zap.Logger) *RuleManager {
	ctx, cancel := context.WithCancel(ctx)
	rm := &RuleManager{
		Rules:          rules,
		Cfg:            cfg,
		cacheTTL:       5 * time.Minute,
		deviceCache:    make(map[string]cachedValue),
		lastAlertTimes: make(map[string]time.Time),
		alertCounts:    make(map[string]int),
		ruleChans:      make(map[string]chan struct{}),
		alertInserter:  inserter,
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
	}

	for i := range rm.Rules {
		rule := &rm.Rules[i]
		if rule.logger == nil {
			rule.logger = logger
		}
		if rm.Rules[i].CooldownPeriod == 0 {
			switch rm.Rules[i].getMaxLevel() {
			case LevelCritical:
				rm.Rules[i].CooldownPeriod = 30 * time.Second
			case LevelError:
				rm.Rules[i].CooldownPeriod = 1 * time.Minute
			default:
				rm.Rules[i].CooldownPeriod = 5 * time.Minute
			}
		}
		ch := make(chan struct{}, 1)
		rm.ruleChans[rule.ID] = ch
		go rm.ruleWorker(rule, ch, cfg)
	}

	return rm
}

// ----------------------------------------------------------------------
// HandleMQTTMessage
// Payload: { "tenant_id": "...", "device_id": "uuid", "data": { "pressure": 45.4 } }
// Filter by device_id in body — not by topic suffix.
// ----------------------------------------------------------------------

func (m *RuleManager) HandleMQTTMessage(topic string, payload []byte, cfg config.Config) {
	var msg MQTTPayload
	if err := json.Unmarshal(payload, &msg); err != nil {
		m.logger.Error("Failed to parse MQTT payload", zap.Error(err))
		return
	}

	// Topic format: telemetry/{tenant_id}/{device_id}/{category}
	// Extract from topic as fallback if not in payload body
	parts := strings.Split(topic, "/")
	if msg.TenantID == "" && len(parts) >= 2 {
		msg.TenantID = parts[1]
	}
	if msg.DeviceID == "" && len(parts) >= 3 {
		msg.DeviceID = parts[2]
	}

	if msg.DeviceID == "" {
		m.logger.Warn("Cannot determine device_id", zap.String("topic", topic))
		return
	}

	if len(msg.Extra) == 0 {
		m.logger.Debug("Empty payload, skipping", zap.String("device_id", msg.DeviceID))
		return
	}

	flatData := msg.FlatData()
	if len(flatData) == 0 {
		m.logger.Debug("No numeric fields after flatten", zap.String("device_id", msg.DeviceID))
		return
	}

	floatData, err := convertToFloat(flatData)
	if err != nil {
		m.logger.Debug("Skipping non-numeric fields",
			zap.String("device_id", msg.DeviceID),
			zap.Error(err))
	}

	m.logger.Debug("MQTT payload parsed",
		zap.String("device_id", msg.DeviceID),
		zap.String("tenant_id", msg.TenantID),
		zap.Any("fields", keysOfFloat(floatData)),
	)

	m.mu.Lock()
	m.deviceCache[msg.DeviceID] = cachedValue{
		value:     floatData,
		timestamp: time.Now(),
	}
	m.mu.Unlock()

	matched := 0
	m.mu.RLock()
	for i := range m.Rules {
		rule := &m.Rules[i]

		if rule.DeviceID == "" {
			m.logger.Warn("Rule has empty device_id — cannot match payload",
				zap.String("rule_id", rule.ID),
				zap.String("rule_device", rule.Device),
				zap.String("hint", "add device_id column to alerts.alert_rules and store UUID from device.device_list"),
			)
			continue
		}

		if rule.DeviceID != msg.DeviceID {
			continue
		}

		if rule.TenantID != "" && rule.TenantID != msg.TenantID {
			m.logger.Debug("Tenant mismatch",
				zap.String("rule_id", rule.ID),
				zap.String("rule_tenant", rule.TenantID),
				zap.String("msg_tenant", msg.TenantID),
			)
			continue
		}

		matched++
		ch, ok := m.ruleChans[rule.ID]
		if !ok {
			continue
		}
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	m.mu.RUnlock()

	m.logger.Debug("Rules signalled",
		zap.String("device_id", msg.DeviceID),
		zap.Int("matched", matched),
		zap.Int("total_rules", len(m.Rules)),
	)
}

// ----------------------------------------------------------------------

func (m *RuleManager) evaluateRule(rule *AlertRule, cfg config.Config) {
	m.mu.RLock()
	cached, exists := m.deviceCache[rule.DeviceID]
	m.mu.RUnlock()

	if !exists {
		m.logger.Debug("No cached data for device", zap.String("device_id", rule.DeviceID))
		return
	}
	if time.Since(cached.timestamp) > m.cacheTTL {
		m.logger.Debug("Cached data expired", zap.String("device_id", rule.DeviceID))
		return
	}

	m.logger.Debug("Evaluating rule",
		zap.String("rule_id", rule.ID),
		zap.String("device_id", rule.DeviceID),
		zap.Any("data_fields", keysOfFloat(cached.value)),
	)

	for _, condition := range rule.Conditions {
		triggered, message := rule.Evaluate(cached.value, condition)

		m.logger.Debug("Condition result",
			zap.String("rule_id", rule.ID),
			zap.String("operator", condition.Operator),
			zap.Bool("triggered", triggered),
		)

		if !triggered {
			continue
		}

		alertKey := fmt.Sprintf("%s_%d", rule.ID, condition.Level)
		if m.shouldTriggerAlert(alertKey, condition.Level) {
			m.logger.Info("Alert triggered",
				zap.String("rule_id", rule.ID),
				zap.String("device_id", rule.DeviceID),
				zap.String("level", getLevelString(condition.Level)),
				zap.String("message", message),
			)
			err := supabase.InsertAlert(cfg, rule.Table, condition.Device, message, rule.Category, rule.Device, rule.TenantID)
			if err != nil {
				m.logger.Error("Failed to insert alert", zap.Error(err))
			}
			m.markAlertTriggered(alertKey, condition.Level)
		}
	}
}

// createRuleSnapshot — kept for test compatibility.
// Returns the cached float data for the rule's device, or nil if missing/expired.
func (m *RuleManager) createRuleSnapshot(rule *AlertRule) map[string]any {
	m.mu.RLock()
	cached, exists := m.deviceCache[rule.DeviceID]
	m.mu.RUnlock()

	if !exists || time.Since(cached.timestamp) > m.cacheTTL {
		return nil
	}

	// Convert map[string]float64 → map[string]any for test assertions
	out := make(map[string]any, len(cached.value))
	for k, v := range cached.value {
		out[k] = v
	}
	return out
}

func (m *RuleManager) UpdateRules(newRules []AlertRule, cfg config.Config) {
	m.logger.Info("Updating rules", zap.Int("count", len(newRules)))

	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancel()
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.Rules = newRules
	m.ruleChans = make(map[string]chan struct{})

	for i := range newRules {
		ch := make(chan struct{}, 1)
		m.ruleChans[newRules[i].ID] = ch
		go m.ruleWorker(&newRules[i], ch, cfg)
	}
}

func (m *RuleManager) ruleWorker(rule *AlertRule, triggerChan chan struct{}, cfg config.Config) {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-triggerChan:
			m.evaluateRule(rule, cfg)
		}
	}
}

func (m *RuleManager) Shutdown() {
	m.cancel()
}

// ----------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------

func convertToFloat(data map[string]any) (map[string]float64, error) {
	out := make(map[string]float64, len(data))
	var firstErr error
	for k, v := range data {
		switch val := v.(type) {
		case float64:
			out[k] = val
		case float32:
			out[k] = float64(val)
		case int:
			out[k] = float64(val)
		case int32:
			out[k] = float64(val)
		case int64:
			out[k] = float64(val)
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				out[k] = f
			} else if firstErr == nil {
				firstErr = fmt.Errorf("field %q: cannot parse %q as float", k, val)
			}
		}
	}
	return out, firstErr
}

func getLevelString(level int) string {
	switch level {
	case LevelCritical:
		return "CRITICAL"
	case LevelError:
		return "ERROR"
	default:
		return "WARNING"
	}
}

func (r *AlertRule) getMaxLevel() int {
	max := 0
	for _, c := range r.Conditions {
		if c.Level > max {
			max = c.Level
		}
	}
	return max
}

func (m *RuleManager) shouldTriggerAlert(alertKey string, level int) bool {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	now := time.Now()
	lastTime, exists := m.lastAlertTimes[alertKey]
	if !exists || now.Sub(lastTime) > m.getCooldown(alertKey, level) {
		return true
	}
	return false
}

func (m *RuleManager) markAlertTriggered(alertKey string, level int) {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	now := time.Now()
	lastTime, exists := m.lastAlertTimes[alertKey]
	baseCooldown := m.getBaseCooldown(level)
	if exists && now.Sub(lastTime) > baseCooldown*4 {
		m.alertCounts[alertKey] = 0
	}
	m.alertCounts[alertKey]++
	m.lastAlertTimes[alertKey] = now
}

func (m *RuleManager) getBaseCooldown(level int) time.Duration {
	switch level {
	case LevelCritical:
		return 30 * time.Second
	case LevelError:
		return 1 * time.Minute
	default:
		return 5 * time.Minute
	}
}

func (m *RuleManager) getCooldown(alertKey string, level int) time.Duration {
	count := m.alertCounts[alertKey]
	base := float64(m.getBaseCooldown(level))
	exp := base * math.Pow(2, float64(count))
	clamped := math.Max(base, math.Min(exp, base*8))
	return time.Duration(clamped)
}

func isValidValue(value any) bool {
	switch v := value.(type) {
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case string:
		return v != "" && v != "0" && v != "0.0"
	case nil:
		return false
	default:
		return true
	}
}

func keysOfFloat(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func extractAddressFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
