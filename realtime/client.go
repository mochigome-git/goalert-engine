package realtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type Client struct {
	Url    string
	ApiKey string

	mu                sync.Mutex
	conn              *websocket.Conn
	closed            chan struct{}
	logger            *zap.Logger
	dialTimeout       time.Duration
	reconnectInterval time.Duration
	heartbeatDuration time.Duration
	heartbeatInterval time.Duration
}

// Create a new Client with user's speicfications
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
	}
}

// Connect the client with the realtime server
func (client *Client) Connect() error {
	if client.isClientAlive() {
		return nil
	}

	// Attempt to dial the server
	err := client.dialServer()
	if err != nil {
		return fmt.Errorf("Cannot connect to the server: %w", err)
	}

	// client is only alive after the connection has been made
	client.mu.Lock()
	client.closed = make(chan struct{})
	client.mu.Unlock()

	go client.startHeartbeats()

	return nil
}

// Disconnect the client from the realtime server
func (client *Client) Disconnect() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if !client.isClientAlive() {
		return nil
	}

	err := client.conn.Close(websocket.StatusNormalClosure, "Closing the connection")
	if err != nil {
		if !client.isConnectionAlive(err) {
			client.logger.Info("Connection has already been terminated")
			close(client.closed)
		} else {
			return fmt.Errorf("Failed to close the connection: %w", err)
		}
	} else {
		close(client.closed)
	}

	return nil
}

// Start sending heartbeats to the server to maintain connection
func (client *Client) startHeartbeats() {
	for client.isClientAlive() {
		err := client.sendHeartbeat()

		if err != nil {
			if client.isConnectionAlive(err) {
				client.logger.Error("Failed to Connect", zap.Error(err))
			} else {
				client.logger.Warn("Error: lost connection with the server")
				client.logger.Info("Attempting to to send hearbeat again")

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				// there should never be an error returned, since it'll keep trying
				_ = client.reconnect(ctx)
			}
		}

		// in case where the client needs to reconnect with the server,
		// the interval between heartbeats be however long it takes to
		// reconnect plus the number of heartbeatInterval has gone by
		time.Sleep(client.heartbeatInterval)
	}
}

// Send the heartbeat to the realtime server
func (client *Client) sendHeartbeat() error {
	msg := HearbeatMsg{
		TemplateMsg: TemplateMsg{
			Event: HEARTBEAT_EVENT,
			Topic: "phoenix",
			Ref:   "",
		},
		Payload: struct{}{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), client.heartbeatDuration)
	defer cancel()

	client.logger.Info("Sending heartbeat")

	err := wsjson.Write(ctx, client.conn, msg)
	if err != nil {
		client.logger.Error("Failed to send heartbeat",
			zap.Float64("timeout_seconds", client.heartbeatDuration.Seconds()),
			zap.Error(err),
		)
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}

	client.logger.Info("Heartbeat sent successfully")
	return nil
}

// Dial the server with a certain timeout in seconds
func (client *Client) dialServer() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.isClientAlive() {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), client.dialTimeout)
	defer cancel()

	//client.logger.Printf("Attempting to connect to: %s", client.Url) // Add this line

	conn, _, err := websocket.Dial(ctx, client.Url, nil)
	if err != nil {
		client.logger.Error("WebSocket dial failed",
			zap.String("url", client.Url),
			zap.Error(err),
		)
		return fmt.Errorf("failed to dial the server: %w", err)
	}

	client.conn = conn
	client.logger.Info("Connection established successfully",
		zap.String("url", client.Url),
	)
	return nil
}

// Keep trying to reconnect every 0.5 seconds until ctx is done/invalidated
func (client *Client) reconnect(ctx context.Context) error {
	for client.isClientAlive() {
		client.logger.Info("Attempting to reconnect to the server")

		select {
		case <-ctx.Done():
			return fmt.Errorf("Failed to reconnect to the server within time limit")
		default:
			err := client.dialServer()
			if err == nil {
				return nil
			}

			client.logger.Warn("Reconnection attempt failed",
				zap.Error(err),
				zap.Duration("retry_after", client.reconnectInterval),
			)
			time.Sleep(client.reconnectInterval)
		}
	}

	return nil
}

// Check if the realtime client has been killed
func (client *Client) isClientAlive() bool {
	if client.closed == nil {
		return false
	}

	select {
	case <-client.closed:
		return false
	default:
		break
	}

	return true
}

// Add to realtime package
type PostgresChangesOptions struct {
	Schema string
	Table  string
	Filter string // e.g., "event=INSERT"
}

func (client *Client) ListenToPostgresChanges(opts PostgresChangesOptions, handler func(payload map[string]interface{})) error {
	if !client.isClientAlive() {
		return errors.New("client not connected")
	}

	// Construct the topic name
	topic := fmt.Sprintf("realtime:%s:%s", opts.Schema, opts.Table)

	// Subscribe message
	subscribeMsg := map[string]interface{}{
		"topic": topic,
		"event": JOIN_EVENT,
		"payload": map[string]interface{}{
			"config": map[string]interface{}{
				"postgres_changes": []map[string]interface{}{
					{
						"event":  opts.Filter, // "INSERT", "UPDATE", "DELETE", or "*"
						"schema": opts.Schema,
						"table":  opts.Table,
					},
				},
			},
		},
		"ref": "1", // can be any string
	}

	// Send subscription
	ctx, cancel := context.WithTimeout(context.Background(), client.dialTimeout)
	defer cancel()

	client.mu.Lock()
	defer client.mu.Unlock()

	if err := wsjson.Write(ctx, client.conn, subscribeMsg); err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	// Start message listener if not already running
	go client.listenForMessages(handler)

	return nil
}

func (client *Client) listenForMessages(handler func(map[string]interface{})) {
	for client.isClientAlive() {
		var msg map[string]interface{}
		ctx := context.Background()

		err := wsjson.Read(ctx, client.conn, &msg)
		if err != nil {
			if !client.isConnectionAlive(err) {
				client.logger.Info("Connection closed, stopping listener")
				return
			}
			continue
		}

		// Filter for postgres_changes events
		if event, ok := msg["event"].(string); ok && event == POSTGRES_CHANGE_EVENT {
			handler(msg)
		}
	}
}

// The underlying package of websocket returns an error if the connection is
// terminated on the server side. Therefore, the state of the connection can
// be achieved by investigating the error
// Constraints: err must be returned from interacting with the connection
func (client *Client) isConnectionAlive(err error) bool {
	return !errors.Is(err, io.EOF)
}
