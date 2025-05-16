# GoAlert Engine

A lightweight alerting engine that processes incoming data via MQTT and evaluates rules to trigger alerts.

## Features

- MQTT-based data ingestion
- Rule-based alert evaluation
- Configurable via environment variables or config file
- Graceful shutdown handling

## Installation

```bash
git clone https://github.com/yourusername/goalert-engine.git
cd goalert-engine
go build -o goalert-engine
```

# Configuration

## Configuration can be provided via:

- Environment variables
- Configuration file (see config.example.yaml)

## Key configuration options:

- MQTT connection details
- Rule definitions
- Alert destinations

# Usage

## Run the engine

```bash
./goalert-engine
```

# Development

## Requirements

- Go 1.20+
- MQTT broker (for testing)

## Environment Setup

1. Copy the example config:

```bash
cp example.env.local .env.local
```

2. Modify the config file with your settings
