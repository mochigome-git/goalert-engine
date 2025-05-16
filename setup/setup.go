package setup

import (
	"context"
	"errors"
	"goalert-engine/alert"
	"goalert-engine/config"
	"goalert-engine/mqtts"
	"goalert-engine/supabase"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"
)

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
	inserter := &supabase.SupabaseInserter{}
	mqttClient := mqtts.New(cfg)

	loader, err := alert.NewSupabaseRuleLoader(cfg)
	if err != nil {
		return nil, nil, err
	}

	rules, err := loader.GetRules(logger)
	if err != nil {
		return nil, nil, err
	}

	if len(rules) == 0 {
		return nil, nil, errors.New("no rules found")
	}

	// Load rules from a file (which contains multiple conditions per rule)
	// loadedRules := alert.LoadRulesFromFile("mocks/rules.json", logger)
	// return alert.NewRuleManager(ctx, loadedRules, cfg, inserter, logger), mqttClient, nil

	return alert.NewRuleManager(ctx, rules, cfg, inserter, logger), mqttClient, nil
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
