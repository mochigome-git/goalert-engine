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
	// Use development config temporarily to see debug logs
	// Switch back to NewProductionConfig() once alert firing is confirmed
	cfg := zap.NewDevelopmentConfig()
	cfg.EncoderConfig.TimeKey = ""
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
		logger.Info("GoAlert Engine", zap.String("version", version))
		return true
	}
	return false
}

func ValidateConfig(cfg config.Config) error {
	if cfg.MQTTTopic == "" {
		return errors.New("MQTT_TOPIC cannot be empty (use # for all topics)")
	}
	if cfg.MQTTBroker == "" {
		return errors.New("MQTT_BROKER cannot be empty")
	}
	if cfg.SupabaseURL == "" {
		return errors.New("SUPABASE_URL cannot be empty")
	}
	return nil
}

func InitializeServices(
	ctx context.Context,
	cfg config.Config,
	logger *zap.Logger,
) (*alert.RuleManager, *mqtts.Client, error) {
	// MQTT client — logger passed in so connect/disconnect events are visible
	mqttClient := mqtts.New(cfg, logger)

	inserter := &supabase.SupabaseInserter{}

	loader, err := alert.NewSupabaseRuleLoader(cfg, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create rule loader: %w", err)
	}

	rules, err := loader.GetRules()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load rules: %w", err)
	}

	logger.Info("Rules loaded", zap.Int("count", len(rules)))

	manager := alert.NewRuleManager(ctx, rules, cfg, inserter, logger)

	err = loader.WatchChanges(ctx, func(updatedRules []alert.AlertRule) {
		logger.Info("Rules updated", zap.Int("count", len(updatedRules)))
		manager.UpdateRules(updatedRules, cfg)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start realtime listener: %w", err)
	}

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
			// logger.Debug("MQTT message received",
			// 	zap.String("topic", msg.Topic()),
			// 	zap.Int("bytes", len(msg.Payload())),
			// )
			ruleManager.HandleMQTTMessage(msg.Topic(), msg.Payload(), cfg)
		}
	}

	if err := mqttClient.SubscribeAndListen(cfg.MQTTTopic, messageHandler); err != nil {
		logger.Error("Failed to subscribe",
			zap.String("topic", cfg.MQTTTopic),
			zap.Error(err),
		)
	}
}
