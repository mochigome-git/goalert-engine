package mqtts

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"goalert-engine/config"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var mqttNewClient = mqtt.NewClient

type Client struct {
	cfg    config.Config
	Client mqtt.Client
	logger *zap.Logger
	// stored so OnConnectHandler can resubscribe after reconnect
	topic   string
	handler mqtt.MessageHandler
}

func (c *Client) AddRoute(topic string, callback mqtt.MessageHandler) {
	c.Client.AddRoute(topic, callback)
}

func New(cfg config.Config, logger *zap.Logger) *Client {
	c := &Client{cfg: cfg, logger: logger}

	clientID := cfg.MQTTClientID
	if clientID == "" {
		clientID = "goalert-engine-" + uuid.New().String()
	}

	opts := mqtt.NewClientOptions().AddBroker(cfg.MQTTBroker)
	opts.SetClientID(clientID)

	if cfg.MQTTUsername != "" {
		opts.SetUsername(cfg.MQTTUsername)
	}
	if cfg.MQTTPassword != "" {
		opts.SetPassword(cfg.MQTTPassword)
	}

	if cfg.TLSCACert != "" {
		tlsConfig, err := createTLSConfig(cfg)
		if err != nil {
			panic(fmt.Errorf("failed to create TLS config: %v", err))
		}
		opts.SetTLSConfig(tlsConfig)
	}

	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(30 * time.Second)
	opts.SetConnectRetry(true)
	// Keep session clean so server doesn't queue messages for us while offline
	opts.SetCleanSession(true)

	// OnConnectHandler fires on EVERY connect — initial and after reconnect.
	// This is the correct place to subscribe so the subscription is always
	// active on the broker regardless of reconnects.
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		r := client.OptionsReader()
		logger.Info("MQTT connected",
			zap.String("broker", cfg.MQTTBroker),
			zap.String("client_id", r.ClientID()),
		)

		// Use stored topic/handler if set (reconnect path),
		// otherwise fall back to cfg.MQTTTopic (initial connect before SubscribeAndListen is called)
		topic := c.topic
		handler := c.handler
		if topic == "" {
			topic = cfg.MQTTTopic
		}
		if topic == "" {
			return
		}
		// handler may be nil on initial connect — paho will use the default handler
		token := client.Subscribe(topic, 0, handler)
		token.Wait()
		if err := token.Error(); err != nil {
			logger.Error("Failed to subscribe on connect",
				zap.String("topic", topic),
				zap.Error(err),
			)
		} else {
			logger.Info("MQTT subscribed", zap.String("topic", topic))
		}
	})

	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		logger.Error("MQTT connection lost — will reconnect",
			zap.Error(err),
			zap.String("broker", cfg.MQTTBroker),
		)
	})

	opts.SetReconnectingHandler(func(client mqtt.Client, opts *mqtt.ClientOptions) {
		logger.Warn("MQTT reconnecting...", zap.String("broker", cfg.MQTTBroker))
	})

	logger.Info("Connecting to MQTT broker",
		zap.String("broker", cfg.MQTTBroker),
		zap.String("client_id", clientID),
		zap.Bool("tls", cfg.TLSCACert != ""),
		zap.Bool("auth", cfg.MQTTUsername != ""),
	)

	c.Client = mqttNewClient(opts)
	token := c.Client.Connect()

	if ok := token.WaitTimeout(15 * time.Second); !ok {
		logger.Fatal("MQTT connect timed out",
			zap.String("broker", cfg.MQTTBroker),
			zap.String("hint", "check broker address, port, TLS certs, and credentials"),
		)
	}
	if err := token.Error(); err != nil {
		logger.Fatal("MQTT connect failed",
			zap.String("broker", cfg.MQTTBroker),
			zap.Error(err),
		)
	}

	return c
}

// SubscribeAndListen stores the topic+handler and subscribes immediately.
// On every reconnect, OnConnectHandler will resubscribe automatically.
func (c *Client) SubscribeAndListen(topic string, handler mqtt.MessageHandler) error {
	// Store for reconnect reuse
	c.topic = topic
	c.handler = handler

	token := c.Client.Subscribe(topic, 0, handler)
	token.Wait()
	if err := token.Error(); err != nil {
		return err
	}
	c.logger.Info("MQTT subscribed", zap.String("topic", topic))
	return nil
}

func (c *Client) Disconnect(quiesce uint) {
	c.logger.Info("Disconnecting MQTT client")
	c.Client.Disconnect(quiesce)
}

func createTLSConfig(cfg config.Config) (*tls.Config, error) {
	caCert := []byte(cfg.TLSCACert)
	if len(caCert) == 0 {
		return nil, fmt.Errorf("TLS_CA_CERT is empty")
	}

	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM(caCert); !ok {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	tlsCfg := &tls.Config{RootCAs: certPool}

	if cfg.TLSClientCert != "" && cfg.TLSClientKey != "" {
		cert, err := tls.X509KeyPair([]byte(cfg.TLSClientCert), []byte(cfg.TLSClientKey))
		if err != nil {
			return nil, fmt.Errorf("failed to load client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}
