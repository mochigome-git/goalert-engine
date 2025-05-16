package alert

import (
	"encoding/json"
	"errors"
	"fmt"
	"goalert-engine/config"
	"io/ioutil"
	"log"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/supabase-community/supabase-go"
	"go.uber.org/zap"
)

type SupabaseRuleLoader struct {
	client    *supabase.Client
	cache     *ristretto.Cache
	ttl       time.Duration
	TableName string
}

func NewSupabaseRuleLoader(cfg config.Config) (*SupabaseRuleLoader, error) {
	apiURL := cfg.Supabase.URL
	apiKey := cfg.Supabase.Key
	tableName := cfg.Supabase.Table
	schema := cfg.Supabase.Schema

	// Initialize cache
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e7,
		MaxCost:     100,
		BufferItems: 64,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cache: %w", err)
	}

	// 👇 Pass schema explicitly
	client, err := supabase.NewClient(apiURL, apiKey, &supabase.ClientOptions{
		Schema: schema,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Supabase client: %w", err)
	}

	return &SupabaseRuleLoader{
		client:    client,
		cache:     cache,
		ttl:       5 * time.Minute,
		TableName: tableName,
	}, nil
}

func (s *SupabaseRuleLoader) GetRules(logger *zap.Logger) ([]AlertRule, error) {
	// Check cache first
	if val, ok := s.cache.Get("all_rules"); ok {
		if rules, ok := val.([]AlertRule); ok {
			return rules, nil
		}
		return nil, errors.New("invalid cache type")
	}

	// Fetch from Supabase
	rules, err := s.loadFromSupabase(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	// Update cache
	s.cache.SetWithTTL("all_rules", rules, 1, s.ttl)
	return rules, nil
}

func (s *SupabaseRuleLoader) loadFromSupabase(logger *zap.Logger) ([]AlertRule, error) {
	var dbRules []struct {
		ID         int              `json:"id"`
		Topics     []string         `json:"topics"`
		Table      string           `json:"table"`
		Field      string           `json:"field"`
		Conditions []AlertCondition `json:"conditions"`
	}

	// Using Supabase's Go client for query
	_, err := s.client.From(s.TableName).Select("*", "", false).ExecuteTo(&dbRules)
	if err != nil {
		return nil, fmt.Errorf("supabase query failed: %w", err)
	}

	// Convert to proper AlertRule with initialization
	rules := make([]AlertRule, len(dbRules))
	for i, dbRule := range dbRules {
		rules[i] = *NewAlertRule(
			dbRule.ID,
			dbRule.Topics,
			dbRule.Table,
			dbRule.Field,
			dbRule.Conditions,
			logger,
		)
	}

	return rules, nil
}

// func (s *SupabaseRuleLoader) WatchChanges(ctx context.Context) error {
// 	subscription := s.client.Realtime().
// 		From(s.tableName).
// 		On("UPDATE", func(payload map[string]interface{}) {
// 			s.cache.Del("all_rules")
// 		}).
// 		On("INSERT", func(payload map[string]interface{}) {
// 			s.cache.Del("all_rules")
// 		}).
// 		On("DELETE", func(payload map[string]interface{}) {
// 			s.cache.Del("all_rules")
// 		}).
// 		Subscribe()
//
// 	go func() {
// 		<-ctx.Done()
// 		subscription.Unsubscribe()
// 	}()
//
// 	return nil
// }

func LoadRulesFromFile(path string, logger *zap.Logger) []AlertRule {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalf("Failed to read rules file: %v", err)
	}

	var fileRules []struct {
		ID             int              `json:"id"`
		Topics         []string         `json:"topics"`
		Table          string           `json:"table"`
		Field          string           `json:"field"`
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
			fileRule.Conditions,
			logger,
		)
	}

	return rules
}
