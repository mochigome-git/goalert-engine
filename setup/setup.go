package setup

import (
	"context"
	"errors"
	"fmt"
	"goalert-engine/alert"
	"goalert-engine/config"
	"goalert-engine/mqtts"
	"goalert-engine/supabase"
	"os"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"
)

func InitLogger() *zap.Logger {
	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.TimeKey = "" // disable "ts" field
	cfg.EncoderConfig.MessageKey = "message"
	cfg.EncoderConfig.LevelKey = "severity"

	logger, err := cfg.Build()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize logger: %v", err))
	}
	return logger
}

func HandleVersionFlag(logger *zap.Logger, version string) bool {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		logger.Info("GoAlert Engine",
			zap.String("version", version))
		return true
	}
	return false
}

func ValidateConfig(cfg config.Config) error {
	if cfg.MQTTTopic == "" {
		return errors.New("MQTT topic cannot be empty")
	}
	return nil
}

func InitializeServices(
	ctx context.Context,
	cfg config.Config,
	logger *zap.Logger,
) (*alert.RuleManager, *mqtts.Client, error) {
	// Initialize MQTT client
	mqttClient := mqtts.New(cfg)

	// Initialize Supabase inserter
	inserter := &supabase.SupabaseInserter{}

	// Initialize rule loader
	loader, err := alert.NewSupabaseRuleLoader(cfg, logger)
	if err != nil {
		return nil, nil, err
	}

	// Load initial rules
	rules, err := loader.GetRules()
	if err != nil {
		return nil, nil, err
	}

	if len(rules) == 0 {
		logger.Warn("no rules found, continuing with empty rule set")
	}

	manager := alert.NewRuleManager(ctx, rules, cfg, inserter, logger)

	// Start watching for changes and update manager on change
	err = loader.WatchChanges(ctx, func(updatedRules []alert.AlertRule) {
		manager.UpdateRules(updatedRules, cfg)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start rule realtime listener: %w", err)
	}

	// Load rules from a file (which contains multiple conditions per rule)
	// loadedRules := alert.LoadRulesFromFile("mocks/rules.json", logger)
	// return alert.NewRuleManager(ctx, loadedRules, cfg, inserter, logger), mqttClient, nil

	return manager, mqttClient, nil
}

func MQTTSubscriber(
	ctx context.Context,
	wg *sync.WaitGroup,
	mqttClient *mqtts.Client,
	ruleManager *alert.RuleManager,
	cfg config.Config,
	logger *zap.Logger,
) {
	messageHandler := func(client mqtt.Client, msg mqtt.Message) {
		wg.Add(1)
		defer wg.Done()

		select {
		case <-ctx.Done():
			return
		default:
			ruleManager.HandleMQTTMessage(msg.Topic(), msg.Payload(), cfg)
		}
	}

	if err := mqttClient.SubscribeAndListen(cfg.MQTTTopic, messageHandler); err != nil {
		logger.Error(
			"Failed to subscribe to MQTT topic",
			zap.String("topic", cfg.MQTTTopic),
			zap.Error(err),
		)
	}
}
