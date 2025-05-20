package main

import (
	"context"
	"goalert-engine/config"
	"goalert-engine/setup"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
)

var (
	version = "dev"
)

func main() {
	// Initialize logger with better configuration
	logger := setup.InitLogger()
	if setup.HandleVersionFlag(logger, version) {
		return
	}

	logger.Info("Starting GoAlert engine", zap.String("version", version))

	// Load and validate configuration
	cfg := config.Load()
	if err := setup.ValidateConfig(cfg); err != nil {
		logger.Fatal("Invalid configuration", zap.Error(err))
	}

	// Set up context with graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize service manager
	serviceManager := setup.NewServiceManager(ctx, cfg, logger)
	if err := serviceManager.Start(); err != nil {
		logger.Fatal("Failed to start services", zap.Error(err))
	}

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

	logger.Info("Shutdown complete")
}
