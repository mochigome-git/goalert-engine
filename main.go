package main

import (
	"context"
	"goalert-engine/config"
	"goalert-engine/setup"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"go.uber.org/zap"
)

var version = "dev"

func main() {
	cfgZap := zap.NewProductionConfig()
	cfgZap.EncoderConfig.TimeKey = "" // disable "ts" field
	logger, _ := cfgZap.Build()
	defer logger.Sync()

	if len(os.Args) > 1 && os.Args[1] == "--version" {
		logger.Info("GoAlert Engine version", zap.String("version", version))
		return
	}

	logger.Info("Starting GoAlert engine...")

	cfg := config.Load()
	if err := setup.ValidateConfig(cfg); err != nil {
		logger.Fatal("Invalid configuration", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ruleManager, mqttClient, err := setup.InitializeServices(ctx, cfg, logger)
	if err != nil {
		logger.Fatal("Failed to initialize services", zap.Error(err))
	}

	var wg sync.WaitGroup
	setup.MQTTSubscriber(ctx, &wg, mqttClient, ruleManager, cfg, logger)

	logger.Info("Service started successfully")

	// Wait for shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigChan:
		logger.Info("Received shutdown signal")
	case <-ctx.Done():
		logger.Info("Context cancelled")
	}

	logger.Info("Waiting for goroutines to finish...")
	wg.Wait()
	mqttClient.Disconnect(250)
	logger.Info("Shutdown complete")
}
