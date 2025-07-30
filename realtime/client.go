package realtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"go.uber.org/zap"
)

type connectionState int

const (
	stateDisconnected connectionState = iota
	stateConnecting
	stateConnected
	stateReconnecting
)

type Client struct {
	Url    string
	ApiKey string

	mu     sync.Mutex
	conn   *websocket.Conn
	closed chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	state  connectionState

	subscriptions   []PostgresChangesOptions
	messageHandlers map[string]func(map[string]any)

	logger            *zap.Logger
	reconnectMu       sync.Mutex
	dialTimeout       time.Duration
	reconnectInterval time.Duration
	heartbeatDuration time.Duration
	heartbeatInterval time.Duration
	heartbeatCancel   context.CancelFunc
}

func CreateRealtimeClient(projectRef string, apiKey string, logger *zap.Logger) *Client {
	realtimeUrl := fmt.Sprintf(
		"wss://%s.supabase.co/realtime/v1/websocket?apikey=%s&log_level=info&vsn=1.0.0",
		projectRef,
		apiKey,
	)

	return &Client{
		Url:               realtimeUrl,
		ApiKey:            apiKey,
		logger:            logger,
		dialTimeout:       10 * time.Second,
		heartbeatDuration: 5 * time.Second,
		heartbeatInterval: 20 * time.Second,
		reconnectInterval: 500 * time.Millisecond,
		state:             stateDisconnected,
		messageHandlers:   make(map[string]func(map[string]any)),
	}
}

// Connect the client with the realtime server
func (client *Client) Connect() error {
	client.mu.Lock()
	// Check both ctx and actual connection state
	if client.ctx != nil && client.state == stateConnected && client.conn != nil {
		client.mu.Unlock()
		return nil
	}
	client.ctx, client.cancel = context.WithCancel(context.Background())
	client.closed = make(chan struct{})
	client.state = stateConnecting
	client.mu.Unlock()

	if err := client.dialServer(); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	client.startHeartbeats()
	go client.listenForMessages()

	client.mu.Lock()
	client.state = stateConnected
	client.mu.Unlock()

	return nil
}

// Disconnect the client from the realtime server
func (client *Client) Disconnect() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.heartbeatCancel != nil {
		client.heartbeatCancel()
		client.heartbeatCancel = nil
	}

	if client.cancel != nil {
		client.cancel()
		client.cancel = nil
		client.ctx = nil
	}

	if client.conn != nil {
		_ = client.conn.Close(websocket.StatusNormalClosure, "client disconnect")
		client.conn = nil
	}

	if client.closed != nil {
		close(client.closed)
		client.closed = nil
	}

	client.state = stateDisconnected
	return nil
}

// Check if the client is connecting to realtime server
func (client *Client) isClientAlive() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.state == stateConnected && client.conn != nil && client.closed != nil
}

// Start sending heartbeats to the server to maintain connection
func (client *Client) startHeartbeats() {
	client.mu.Lock()

	// Cancel old heartbeat loop if any
	if client.heartbeatCancel != nil {
		client.logger.Info("Stop heartbeat...")
		client.heartbeatCancel() // stop previous loop
	}
	ctx := client.ctx
	if ctx == nil {
		// prevent goroutine leak
		client.mu.Unlock()
		return
	}

	// Create new cancelable context for the new heartbeat loop
	hbCtx, cancel := context.WithCancel(ctx)
	client.heartbeatCancel = cancel
	client.mu.Unlock()

	go client.heartbeatLoop(hbCtx)
}

// Start loop to sending heartbeats
func (client *Client) heartbeatLoop(ctx context.Context) {
	retryInterval := client.heartbeatInterval

	for {
		if ctx.Err() != nil {
			return
		}

		err := client.sendHeartbeat()
		if err != nil {
			client.logger.Error("Heartbeat failed", zap.Error(err))
			_ = client.Disconnect()
			time.Sleep(retryInterval)
			client.reconnect(context.Background())

			// Increase backoff time but cap at 30s
			retryInterval = time.Duration(math.Min(float64(retryInterval*2), float64(30*time.Second)))
			continue
		}

		// Reset backoff on success
		retryInterval = client.heartbeatInterval

		select {
		case <-ctx.Done():
			return

		// in case where the client needs to reconnect with the server,
		// the interval between heartbeats be however long it takes to
		// reconnect plus the number of heartbeatInterval has gone by
		case <-time.After(client.heartbeatInterval):
		}
	}
}

// Send the heartbeat to the realtime server
func (client *Client) sendHeartbeat() error {
	client.mu.Lock()
	conn := client.conn
	ctx := client.ctx
	client.mu.Unlock()

	if conn == nil {
		return errors.New("no active connection")
	}

	heartbeat := HearbeatMsg{
		TemplateMsg: TemplateMsg{
			Event: HEARTBEAT_EVENT,
			Topic: "phoenix",
			Ref:   "",
		},
		Payload: struct{}{},
	}

	heartbeatCtx, cancel := context.WithTimeout(ctx, client.heartbeatDuration)
	defer cancel()

	client.logger.Debug("Sending heartbeat")

	if err := wsjson.Write(heartbeatCtx, conn, heartbeat); err != nil {
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}
	return nil
}

// Dial the server with a certain timeout in seconds
func (client *Client) dialServer() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), client.dialTimeout)
	defer cancel()

	//client.logger.Printf("Attempting to connect to: %s", client.Url)

	conn, _, err := websocket.Dial(ctx, client.Url, nil)
	if err != nil {
		client.logger.Error("Dial failed", zap.Error(err))
		return err
	}

	client.conn = conn
	client.logger.Info("WebSocket connected", zap.String("url", client.Url))
	return nil
}

// Keep trying to reconnect every 0.5 seconds until ctx is done/invalidated
func (client *Client) reconnect(ctx context.Context) {
	client.reconnectMu.Lock()
	defer client.reconnectMu.Unlock()

	client.mu.Lock()
	client.state = stateReconnecting
	client.mu.Unlock()

	client.logger.Warn("Starting reconnect loop...")

	retryTicker := time.NewTicker(client.reconnectInterval)
	defer retryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			client.logger.Warn("Reconnect context done, giving up")
			return
		case <-retryTicker.C:
			if err := client.Connect(); err == nil {
				client.logger.Info("Reconnected successfully")
				client.resubscribeAll()
				return
			} else {
				client.logger.Warn("Reconnect failed", zap.Error(err))
			}
		}
	}
}

// Check if the realtime client has been killed
func (client *Client) resubscribeAll() {
	client.mu.Lock()
	subs := append([]PostgresChangesOptions(nil), client.subscriptions...)
	client.mu.Unlock()

	for _, sub := range subs {
		_ = client.sendSubscription(sub)
	}
}

// Listen to the Realtime server and monitoring if postgres changes triggered
func (client *Client) ListenToPostgresChanges(opts PostgresChangesOptions, handler func(map[string]any)) error {
	if !client.isClientAlive() {
		return errors.New("client not connected")
	}

	topic := fmt.Sprintf("realtime:%s:%s", opts.Schema, opts.Table)

	client.mu.Lock()
	client.subscriptions = append(client.subscriptions, opts)
	client.messageHandlers[topic] = handler
	client.mu.Unlock()

	return client.sendSubscription(opts)
}

func (client *Client) sendSubscription(opts PostgresChangesOptions) error {
	topic := fmt.Sprintf("realtime:%s:%s", opts.Schema, opts.Table)
	subscribeMsg := map[string]any{
		"topic": topic,
		"event": JOIN_EVENT,
		"payload": map[string]any{
			"config": map[string]any{
				"postgres_changes": []map[string]any{
					{
						"event":  opts.Filter, // "INSERT", "UPDATE", "DELETE", or "*"
						"schema": opts.Schema,
						"table":  opts.Table,
					},
				},
			},
		},
		"ref": "1",
	}

	client.mu.Lock()
	conn := client.conn
	ctx := client.ctx
	client.mu.Unlock()

	if conn == nil {
		return errors.New("sendSubscription: no active connection")
	}

	ctx, cancel := context.WithTimeout(ctx, client.dialTimeout)
	defer cancel()

	return wsjson.Write(ctx, conn, subscribeMsg)
}

// Listen to the changes messages in realtime server
func (client *Client) listenForMessages() {
	for {
		client.mu.Lock()
		ctx := client.ctx
		conn := client.conn
		client.mu.Unlock()

		if ctx == nil || ctx.Err() != nil {
			client.logger.Info("Listener exiting")
			return
		}

		var msg map[string]any
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			if !client.isConnectionAlive(err) {
				client.logger.Warn("Connection lost during message read")
				go client.reconnect(context.Background())
				return
			}
			continue
		}

		if event, ok := msg["event"].(string); ok && event == POSTGRES_CHANGE_EVENT {
			topic, _ := msg["topic"].(string)

			client.mu.Lock()
			handler, exists := client.messageHandlers[topic]
			client.mu.Unlock()

			if exists {
				go handler(msg)
			} else {
				client.logger.Warn("No handler found for topic", zap.String("topic", topic))
			}
		}
	}
}

// The underlying package of websocket returns an error if the connection is
// terminated on the server side. Therefore, the state of the connection can
// be achieved by investigating the error
// Constraints: err must be returned from interacting with the connection
func (client *Client) isConnectionAlive(err error) bool {
	return !(errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, new(net.Error)) ||
		errors.As(err, new(websocket.CloseError)))
}
