package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"goalert-engine/config"
	"goalert-engine/supabase"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

type cachedValue struct {
	value     any
	timestamp time.Time
}

type cacheKey struct {
	Topic   string
	Address string
}

type AlertInserter interface {
	InsertAlert(cfg config.Config, table, device, message string) error
}

type RuleManager struct {
	Rules          []AlertRule
	Cfg            config.Config
	ruleChans      map[int]chan struct{}
	deviceCache    map[cacheKey]cachedValue // Store values with timestamps
	mu             sync.RWMutex             // Use RWMutex for better read performance
	cacheTTL       time.Duration            // How long values stay in cache
	lastAlertTimes map[string]time.Time     // ruleID -> last alert time
	alertCounts    map[string]int           // ruleID -> alert count
	alertMu        sync.Mutex               // Mutex for alert tracking
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
		deviceCache:    make(map[cacheKey]cachedValue),
		lastAlertTimes: make(map[string]time.Time),
		alertCounts:    make(map[string]int),
		ruleChans:      make(map[int]chan struct{}),
		alertInserter:  inserter,
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
	}

	// Initialize default cooldown periods if not set
	for i := range rm.Rules {
		rule := &rm.Rules[i]
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

		ch := make(chan struct{}, 1) // buffered channel to avoid blocking
		rm.ruleChans[rule.ID] = ch
		go rm.ruleWorker(rule, ch, cfg)
	}

	return rm
}

func (m *RuleManager) HandleMQTTMessage(topic string, payload []byte, cfg config.Config) {
	var msg map[string]any
	if err := json.Unmarshal(payload, &msg); err != nil {
		m.logger.Error("Failed to parse payload", zap.Error(err))
		return
	}

	address, ok := msg["address"].(string)
	if !ok {
		m.logger.Warn("Payload missing 'address' field", zap.Any("payload", msg))
		return
	}

	value, ok := msg["value"]
	if !ok {
		m.logger.Warn("Payload missing 'value' field", zap.Any("payload", msg))
		return
	}

	if !isValidValue(value) {
		return
	}

	if extractAddressFromTopic(topic) != address {
		m.logger.Warn("Topic-address mismatch",
			zap.String("topic", topic),
			zap.String("address", address),
			zap.Any("payload", msg),
		)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock() // Use defer to ensure unlock

	if m.deviceCache == nil {
		m.deviceCache = make(map[cacheKey]cachedValue)
	}

	key := cacheKey{
		Topic:   topic,
		Address: address,
	}

	now := time.Now()

	// Always update the cache with new values
	m.deviceCache[key] = cachedValue{
		value:     value,
		timestamp: now,
	}

	// Signal relevant rules
	for i := range m.Rules {
		rule := &m.Rules[i]
		if slices.Contains(rule.Topics, topic) {
			ch, ok := m.ruleChans[rule.ID]
			if !ok {
				m.logger.Warn("Rule channel missing", zap.Int("ruleID", rule.ID))
				continue
			}
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

func (m *RuleManager) evaluateRule(rule *AlertRule, cfg config.Config) {
	// Create a snapshot of the required device values
	snapshot := m.createRuleSnapshot(rule)

	if snapshot != nil {
		m.logger.Info("DEBUG: Evaluating rule",
			zap.Any("id", rule.ID),
			zap.Any("payload", snapshot),
		)

		for _, condition := range rule.Conditions {
			triggered, message := rule.Evaluate(snapshot, condition)
			if triggered {
				alertKey := fmt.Sprintf("%d_%d", rule.ID, condition.Level)

				if m.shouldTriggerAlert(alertKey, condition.Level) {
					m.logger.Info(
						"Triggered alert",
						zap.Any("Level", getLevelString(condition.Level)),
						zap.String("message", message),
					)
					// Insert the alert into the database
					err := supabase.InsertAlert(cfg, rule.Table, condition.Device, message)
					if err != nil {
						m.logger.Error("Failed to insert alert", zap.Error(err))
					}

					m.markAlertTriggered(alertKey, condition.Level)
				}
			}
		}
	}
}

func (m *RuleManager) createRuleSnapshot(rule *AlertRule) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := make(map[string]any)
	now := time.Now()

	for _, ruleTopic := range rule.Topics {
		devAddr := extractAddressFromTopic(ruleTopic)
		key := cacheKey{Topic: ruleTopic, Address: devAddr}
		cached, exists := m.deviceCache[key]

		// Skip if value doesn't exist or is expired
		if !exists || now.Sub(cached.timestamp) > m.cacheTTL || !isValidValue(cached.value) {
			return nil
		}

		snapshot[devAddr] = cached.value
	}

	// fmt.Printf("Snapshot for rule %d:\n", rule.ID)
	// for k, v := range snapshot {
	// 	fmt.Printf(" - %s: %v (age: %v)\n", k, v, now.Sub(m.deviceCache[k].timestamp))
	// }

	// Only return snapshot if we have all required values
	if len(snapshot) == len(rule.Topics) {
		return snapshot
	}
	return nil
}

func (m *RuleManager) UpdateRules(newRules []AlertRule, cfg config.Config) {
	m.logger.Info("Updating rules", zap.Int("newRuleCount", len(newRules)))

	m.mu.Lock()
	defer m.mu.Unlock()

	// First cancel old context to shut down old workers
	m.cancel()

	// Create a new context for new workers
	m.ctx, m.cancel = context.WithCancel(context.Background())

	// Reset everything from scratch
	m.Rules = newRules
	m.ruleChans = make(map[int]chan struct{})

	// Start a worker for each new rule
	for _, r := range newRules {
		ch := make(chan struct{}, 1)
		m.ruleChans[r.ID] = ch
		go m.ruleWorker(&r, ch, cfg)
	}

	m.logger.Info("Rules updated and workers restarted", zap.Int("count", len(newRules)))
}

func (m *RuleManager) ruleWorker(rule *AlertRule, triggerChan chan struct{}, cfg config.Config) {
	for {
		select {
		case <-m.ctx.Done():
			m.logger.Info("Shutting down rule worker", zap.Int("ruleID", rule.ID))
			return
		case <-triggerChan:
			m.evaluateRule(rule, cfg)
		}
	}
}

func (m *RuleManager) Shutdown() {
	m.cancel()
	m.logger.Info("RuleManager shutdown initiated")
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

// Get maximum alert level for a rule
func (r *AlertRule) getMaxLevel() int {
	max := 0
	for _, cond := range r.Conditions {
		if cond.Level > max {
			max = cond.Level
		}
	}
	return max
}

func (m *RuleManager) shouldTriggerAlert(alertKey string, level int) bool {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()

	now := time.Now()
	lastTime, exists := m.lastAlertTimes[alertKey]

	// Cooldown Checks
	//log.Printf("Cooldown check for alertKey=%s, level=%d, count=%d\n", alertKey, level, m.alertCounts[alertKey])

	// First time alert or cooldown expired
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

	// Reset count if last alert was long ago (e.g., > 4x base cooldown)
	baseCooldown := m.getBaseCooldown(level)
	if exists && now.Sub(lastTime) > (baseCooldown*4) {
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
	baseCooldown := m.getBaseCooldown(level)

	// Exponential backoff with max cooldown of 8x base
	maxCooldown := baseCooldown * 8
	expCooldown := float64(baseCooldown) * math.Pow(2, float64(count))
	clampedCooldown := math.Max(float64(baseCooldown), math.Min(expCooldown, float64(maxCooldown)))

	return time.Duration(clampedCooldown)
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

func extractAddressFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
