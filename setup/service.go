package setup

import (
	"context"
	"fmt"
	"goalert-engine/alert"
	"goalert-engine/config"
	"goalert-engine/mqtts"
	"sync"

	"go.uber.org/zap"
)

type ServiceManager struct {
	ctx                context.Context
	cfg                config.Config
	logger             *zap.Logger
	currentRuleManager *alert.RuleManager
	currentMQTTClient  *mqtts.Client
	restartChan        chan struct{}
	mu                 sync.Mutex
}

func NewServiceManager(ctx context.Context, cfg config.Config, logger *zap.Logger) *ServiceManager {
	return &ServiceManager{
		ctx:         ctx,
		cfg:         cfg,
		logger:      logger,
		restartChan: make(chan struct{}, 1),
	}
}

func (sm *ServiceManager) Start() error {
	return sm.restartServices()
}

func (sm *ServiceManager) restartServices() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Clean up old services if they exist
	if sm.currentRuleManager != nil {
		sm.currentRuleManager.Shutdown()
	}
	if sm.currentMQTTClient != nil {
		sm.currentMQTTClient.Disconnect(250)
	}

	// Initialize new services
	ruleManager, mqttClient, err := InitializeServices(sm.ctx, sm.cfg, sm.logger)
	if err != nil {
		return fmt.Errorf("failed to restart services: %w", err)
	}

	sm.currentRuleManager = ruleManager
	sm.currentMQTTClient = mqttClient

	// Start MQTT subscriber
	var wg sync.WaitGroup
	MQTTSubscriber(sm.ctx, &wg, mqttClient, ruleManager, sm.cfg, sm.logger)

	return nil
}

func (sm *ServiceManager) GetServices() (*alert.RuleManager, *mqtts.Client) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.currentRuleManager, sm.currentMQTTClient
}
