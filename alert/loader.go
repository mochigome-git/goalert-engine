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

	"goalert-engine/realtime"

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
	u, err := url.Parse(cfg.Supabase.URL)
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

	client, err := supabase.NewClient(cfg.Supabase.URL, cfg.Supabase.Key, &supabase.ClientOptions{
		Schema: cfg.Supabase.Schema,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Supabase client: %w", err)
	}

	rtClient := realtime.CreateRealtimeClient(projectRef, cfg.Supabase.Key, logger)
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
		schema:            cfg.Supabase.Schema,
		TableName:         cfg.Supabase.Table,
		RealtimeTableName: cfg.Supabase.Realtime,
		ForeignKey:        cfg.Supabase.ForeignKey,
		ForeignKeyCheck:   cfg.Supabase.ForeignKeyCheck,
	}, nil
}

func (s *SupabaseRuleLoader) WatchChanges(ctx context.Context, onUpdate func([]AlertRule)) error {
	reload := func(source string) {
		s.cache.Del("all_rules")
		updatedRules, err := s.GetRules()
		if err != nil {
			s.logger.Error("Failed to reload rules", zap.String("source", source), zap.Error(err))
			return
		}
		onUpdate(updatedRules)
	}

	// Watch alert_rules — rule created/updated/deleted
	err := s.realtime.ListenToPostgresChanges(realtime.PostgresChangesOptions{
		Schema: s.schema,
		Table:  "alert_rules",
		Filter: "",
	}, func(payload map[string]any) {
		s.logger.Info("alert_rules changed — reloading rules")
		reload("alert_rules")
	})
	if err != nil {
		return fmt.Errorf("failed to watch alert_rules: %w", err)
	}

	// Watch model_group — enabled toggle fires here
	err = s.realtime.ListenToPostgresChanges(realtime.PostgresChangesOptions{
		Schema: s.schema,
		Table:  "model_group",
		Filter: "",
	}, func(payload map[string]any) {
		s.logger.Info("model_group changed — reloading rules")
		reload("model_group")
	})
	if err != nil {
		return fmt.Errorf("failed to watch model_group: %w", err)
	}

	go func() {
		<-ctx.Done()
		s.logger.Info("Stopping realtime watcher")
	}()

	return nil
}

func (s *SupabaseRuleLoader) GetRules() ([]AlertRule, error) {
	if val, ok := s.cache.Get("all_rules"); ok {
		if rules, ok := val.([]AlertRule); ok {
			s.logger.Info("Rules loaded from cache", zap.Int("count", len(rules)))
			return rules, nil
		}
		return nil, errors.New("invalid cache type")
	}

	rules, err := s.loadFromSupabase()
	if err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	s.logger.Info("Rules loaded from Supabase", zap.Int("count", len(rules)))
	s.cache.SetWithTTL("all_rules", rules, 1, s.ttl)
	return rules, nil
}

func (s *SupabaseRuleLoader) loadFromSupabase() ([]AlertRule, error) {
	// Only load rules that belong to an ENABLED model group.
	// Join: alert_rules → model_group_rules → model_group (enabled = true)
	var dbRules []struct {
		ID         string           `json:"id"`
		TenantID   string           `json:"tenant_id"`
		DeviceID   string           `json:"device_id"`
		Topics     []string         `json:"topics"`
		Table      string           `json:"table"`
		Field      string           `json:"field"`
		Category   string           `json:"category"`
		Device     string           `json:"device"`
		Conditions []AlertCondition `json:"conditions"`
	}

	_, err := s.client.
		From(s.TableName).
		Select("*, model_group_rules!inner(*), model_group!inner(*)", "", false).
		Eq("model_group.enabled", "true").
		ExecuteTo(&dbRules)
	if err != nil {
		s.logger.Error("Supabase query failed",
			zap.String("table", s.TableName),
			zap.String("schema", s.schema),
			zap.Error(err),
		)
		return nil, fmt.Errorf("supabase query failed: %w", err)
	}

	s.logger.Info("Fetched rules from DB", zap.Int("count", len(dbRules)))

	rules := make([]AlertRule, len(dbRules))
	for i, r := range dbRules {
		rules[i] = *NewAlertRule(
			r.ID,
			r.TenantID,
			r.DeviceID,
			r.Topics,
			r.Table,
			r.Field,
			r.Category,
			r.Device,
			r.Conditions,
			s.logger,
		)
	}

	return rules, nil
}

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
		ID         string           `json:"id"`
		TenantID   string           `json:"tenant_id"`
		DeviceID   string           `json:"device_id"`
		Topics     []string         `json:"topics"`
		Table      string           `json:"table"`
		Field      string           `json:"field"`
		Category   string           `json:"category"`
		Device     string           `json:"device"`
		Conditions []AlertCondition `json:"conditions"`
	}

	if err := json.Unmarshal(data, &fileRules); err != nil {
		log.Fatalf("Failed to unmarshal rules: %v", err)
	}

	rules := make([]AlertRule, len(fileRules))
	for i, r := range fileRules {
		rules[i] = *NewAlertRule(r.ID, r.TenantID, r.DeviceID, r.Topics, r.Table, r.Field, r.Category, r.Device, r.Conditions, logger)
	}
	return rules
}
