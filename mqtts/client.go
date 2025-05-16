package mqtts

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"goalert-engine/config"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

var mqttNewClient = mqtt.NewClient

type Client struct {
	cfg    config.Config
	Client mqtt.Client
}

func (c *Client) AddRoute(topic string, callback mqtt.MessageHandler) {
	c.Client.AddRoute(topic, callback)
}

func New(cfg config.Config) *Client {
	// MQTT over TLS
	opts := mqtt.NewClientOptions().AddBroker(cfg.MQTTBroker)
	opts.SetClientID("alert-engine")
	opts.SetAutoReconnect(true)                    // Enable automatic reconnects
	opts.SetMaxReconnectInterval(30 * time.Second) // Maximum interval between reconnections
	opts.SetConnectRetry(true)                     // Retry connecting

	// Enable TLS (MQTTS) using certs and keys from environment variables
	tlsConfig, err := createTLSConfig(cfg)
	if err != nil {
		panic(fmt.Errorf("failed to create mqtts TLS config: %v", err))
	}
	clientID := "go_mqtt_subscriber_" + uuid.New().String()
	opts.SetTLSConfig(tlsConfig)
	opts.SetClientID(clientID)
	opts.SetUsername("emqx")
	opts.SetPassword("public")

	// Connect with MQTTS
	client := mqttNewClient(opts)
	token := client.Connect()
	token.Wait()
	if token.Error() != nil {
		panic(token.Error())
	}

	return &Client{
		cfg:    cfg,
		Client: client,
	}
}

// createTLSConfig will load the necessary certificates for MQTTS from environment variables
func createTLSConfig(cfg config.Config) (*tls.Config, error) {
	// Load certificate authorities from environment variable
	caCert := []byte(cfg.TLSCACert) // CA certificate as a string (PEM format)
	if len(caCert) == 0 {
		return nil, fmt.Errorf("CA certificate is not provided")
	}

	// Load the CA certificate into a cert pool
	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM(caCert); !ok {
		return nil, fmt.Errorf("failed to append CA certificate")
	}

	// Check if client cert and key are provided
	if len(cfg.TLSClientCert) == 0 || len(cfg.TLSClientKey) == 0 {
		return nil, fmt.Errorf("client certificate or key is missing")
	}

	// Load client certificate and key into a tls.Certificate
	cert, err := tls.X509KeyPair([]byte(cfg.TLSClientCert), []byte(cfg.TLSClientKey))
	if err != nil {
		return nil, fmt.Errorf("error loading client certificate or key: %s", err)
	}

	// Create and return the TLS configuration
	return &tls.Config{
		RootCAs:      certPool,
		Certificates: []tls.Certificate{cert},
	}, nil
}

// SubscribeAndListen subscribes to the topic and handles incoming messages
func (c *Client) SubscribeAndListen(topic string, handler mqtt.MessageHandler) error {
	token := c.Client.Subscribe(topic, 0, handler)
	token.Wait()
	if token.Error() != nil {
		return token.Error()
	}
	return nil
}

// Disconnect gracefully disconnects from the MQTT broker
func (c *Client) Disconnect(quiesce uint) {
	c.Client.Disconnect(quiesce)
}
