package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"goalert-engine/config"
	"io/ioutil"
	"log"
	"net/url"
	"strings"
	"time"

	"goalert-engine/realtime" // Import your realtime package

	"github.com/dgraph-io/ristretto"
	"github.com/supabase-community/supabase-go"
	"go.uber.org/zap"
)

type SupabaseRuleLoader struct {
	client            *supabase.Client
	cache             *ristretto.Cache
	ttl               time.Duration
	TableName         string
	logger            *zap.Logger
	realtime          *realtime.Client
	projectRef        string
	schema            string
	ForeignKey        string
	ForeignKeyCheck   string
	RealtimeTableName string
}

func NewSupabaseRuleLoader(cfg config.Config, logger *zap.Logger) (*SupabaseRuleLoader, error) {
	apiURL := cfg.Supabase.URL
	apiKey := cfg.Supabase.Key
	schema := cfg.Supabase.Schema

	// Extract project reference from URL
	u, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Supabase URL: %w", err)
	}
	projectRef := strings.TrimSuffix(strings.TrimPrefix(u.Hostname(), "db."), ".supabase.co")

	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e7,
		MaxCost:     100,
		BufferItems: 64,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cache: %w", err)
	}

	client, err := supabase.NewClient(apiURL, apiKey, &supabase.ClientOptions{
		Schema: schema,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Supabase client: %w", err)
	}

	rtClient := realtime.CreateRealtimeClient(projectRef, apiKey, logger)

	// Connect the realtime client
	if err := rtClient.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to realtime service: %w", err)
	}

	return &SupabaseRuleLoader{
		client:            client,
		cache:             cache,
		ttl:               5 * time.Minute,
		logger:            logger,
		realtime:          rtClient,
		projectRef:        projectRef,
		schema:            schema,
		TableName:         cfg.Supabase.Table,
		RealtimeTableName: cfg.Supabase.Realtime,
		ForeignKey:        cfg.Supabase.ForeignKey,
		ForeignKeyCheck:   cfg.Supabase.ForeignKeyCheck,
	}, nil
}

func (s *SupabaseRuleLoader) WatchChanges(ctx context.Context, onUpdate func([]AlertRule)) error {
	// Subscribe to PostgreSQL changes directly
	err := s.realtime.ListenToPostgresChanges(realtime.PostgresChangesOptions{
		Schema: s.schema,
		Table:  s.RealtimeTableName,
		Filter: "*", // Listen to all changes
	}, func(payload map[string]any) {
		s.logger.Info("Database change detected",
			zap.Any("payload", payload))

		// Extract change type and record
		if data, ok := payload["payload"].(map[string]any); ok {
			changeType, _ := data["type"].(string)

			var record map[string]any

			if r, ok := data["record"].(map[string]any); ok {
				record = r
			} else if or, ok := data["old_record"].(map[string]any); ok {
				record = or
			} else {
				record = nil
			}

			s.logger.Debug("Change details",
				zap.String("type", changeType),
				zap.Any("record", record))

			// Invalidate cache and reload rules on any change event
			s.cache.Del("all_rules")
			updatedRules, err := s.GetRules()
			if err != nil {
				s.logger.Error("Failed to reload rules after DB change", zap.Error(err))
				return
			}
			onUpdate(updatedRules)
		}

		// Invalidate cache and reload rules
		s.cache.Del("all_rules")
		updatedRules, err := s.GetRules()
		if err != nil {
			s.logger.Error("Failed to reload rules", zap.Error(err))
			return
		}
		onUpdate(updatedRules)
	})

	if err != nil {
		return fmt.Errorf("failed to listen to postgres changes: %w", err)
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		s.logger.Info("Stopping realtime changes watcher")
		// The connection will be closed when the client is garbage collected
		// or you can explicitly call s.realtime.Disconnect() if needed
	}()

	return nil
}

func (s *SupabaseRuleLoader) GetRules() ([]AlertRule, error) {
	if val, ok := s.cache.Get("all_rules"); ok {
		if rules, ok := val.([]AlertRule); ok {
			return rules, nil
		}
		return nil, errors.New("invalid cache type")
	}

	rules, err := s.loadFromSupabase()
	if err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	s.cache.SetWithTTL("all_rules", rules, 1, s.ttl)
	return rules, nil
}

func (s *SupabaseRuleLoader) loadFromSupabase() ([]AlertRule, error) {
	var dbRules []struct {
		ID         string           `json:"id"`
		Topics     []string         `json:"topics"`
		Table      string           `json:"table"`
		Field      string           `json:"field"`
		Category   string           `json:"category"`
		Machine    string           `json:"machine"`
		Conditions []AlertCondition `json:"conditions"`
	}

	_, err := s.client.
		From(s.TableName).
		Select(s.ForeignKey, "", false).
		Eq(fmt.Sprintf("%s.%s", s.RealtimeTableName, s.ForeignKeyCheck), "true").
		ExecuteTo(&dbRules)
	if err != nil {
		return nil, fmt.Errorf("supabase query failed: %w", err)
	}

	rules := make([]AlertRule, len(dbRules))
	for i, dbRule := range dbRules {
		rules[i] = *NewAlertRule(
			dbRule.ID,
			dbRule.Topics,
			dbRule.Table,
			dbRule.Field,
			dbRule.Category,
			dbRule.Machine,
			dbRule.Conditions,
			s.logger,
		)
	}

	return rules, nil
}

// Close cleans up resources
func (s *SupabaseRuleLoader) Close() error {
	if s.realtime != nil {
		return s.realtime.Disconnect()
	}
	return nil
}

func LoadRulesFromFile(path string, logger *zap.Logger) []AlertRule {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalf("Failed to read rules file: %v", err)
	}

	var fileRules []struct {
		ID             string           `json:"id"`
		Topics         []string         `json:"topics"`
		Table          string           `json:"table"`
		Field          string           `json:"field"`
		Category       string           `json:"category"`
		Machine        string           `json:"machine"`
		Conditions     []AlertCondition `json:"conditions"`
		ThrottlePeriod int              `json:"throttle_period"`
	}

	if err := json.Unmarshal(data, &fileRules); err != nil {
		log.Fatalf("Failed to unmarshal rules: %v", err)
	}

	// Convert to proper AlertRule with initialization
	rules := make([]AlertRule, len(fileRules))
	for i, fileRule := range fileRules {
		rules[i] = *NewAlertRule(
			fileRule.ID,
			fileRule.Topics,
			fileRule.Table,
			fileRule.Field,
			fileRule.Category,
			fileRule.Machine,
			fileRule.Conditions,
			logger,
		)
	}

	return rules
}
