package mqtts

import (
	"crypto/tls"
	"errors"
	"goalert-engine/config"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockClient is a mock implementation of mqtt.Client
type MockClient struct {
	mock.Mock
}

func (m *MockClient) IsConnected() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *MockClient) IsConnectionOpen() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *MockClient) Connect() mqtt.Token {
	args := m.Called()
	return args.Get(0).(mqtt.Token)
}

func (m *MockClient) Disconnect(quiesce uint) {
	m.Called(quiesce)
}

func (m *MockClient) Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	args := m.Called(topic, qos, retained, payload)
	return args.Get(0).(mqtt.Token)
}

func (m *MockClient) Subscribe(topic string, qos byte, callback mqtt.MessageHandler) mqtt.Token {
	args := m.Called(topic, qos, callback)
	return args.Get(0).(mqtt.Token)
}

func (m *MockClient) SubscribeMultiple(filters map[string]byte, callback mqtt.MessageHandler) mqtt.Token {
	args := m.Called(filters, callback)
	return args.Get(0).(mqtt.Token)
}

func (m *MockClient) Unsubscribe(topics ...string) mqtt.Token {
	args := m.Called(topics)
	return args.Get(0).(mqtt.Token)
}

func (m *MockClient) AddRoute(topic string, callback mqtt.MessageHandler) {
	m.Called(topic, callback)
}

func (m *MockClient) OptionsReader() mqtt.ClientOptionsReader {
	args := m.Called()
	return args.Get(0).(mqtt.ClientOptionsReader)
}

// MockToken is a mock implementation of mqtt.Token
type MockToken struct {
	mock.Mock
}

func (m *MockToken) Wait() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *MockToken) WaitTimeout(d time.Duration) bool {
	args := m.Called(d)
	return args.Bool(0)
}

func (m *MockToken) Done() <-chan struct{} {
	args := m.Called()
	return args.Get(0).(<-chan struct{})
}

func (m *MockToken) Error() error {
	args := m.Called()
	return args.Error(0)
}

func TestNewClient(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Config
		mockSetup   func(*MockClient, *MockToken)
		expectError bool
	}{
		{
			name: "successful connection",
			cfg: config.Config{
				MQTTBroker:    "tls://localhost:8883",
				TLSCACert:     validCACert,
				TLSClientCert: validClientCert,
				TLSClientKey:  validClientKey,
			},
			mockSetup: func(mc *MockClient, mt *MockToken) {
				mt.On("Wait").Return(true)
				mt.On("Error").Return(nil)
				mc.On("Connect").Return(mt)
			},
			expectError: false,
		},
		{
			name: "connection error",
			cfg: config.Config{
				MQTTBroker:    "tls://localhost:8883",
				TLSCACert:     "test-ca-cert",
				TLSClientCert: "test-client-cert",
				TLSClientKey:  "test-client-key",
			},
			mockSetup: func(mc *MockClient, mt *MockToken) {
				mt.On("Wait").Return(true)
				mt.On("Error").Return(errors.New("connection failed"))
				mc.On("Connect").Return(mt)
			},
			expectError: true,
		},
		{
			name: "missing CA cert",
			cfg: config.Config{
				MQTTBroker:    "tls://localhost:8883",
				TLSClientCert: "test-client-cert",
				TLSClientKey:  "test-client-key",
			},
			mockSetup:   nil,
			expectError: true,
		},
		{
			name: "missing client cert",
			cfg: config.Config{
				MQTTBroker:   "tls://localhost:8883",
				TLSCACert:    "test-ca-cert",
				TLSClientKey: "test-client-key",
			},
			mockSetup:   nil,
			expectError: true,
		},
		{
			name: "missing client key",
			cfg: config.Config{
				MQTTBroker:    "tls://localhost:8883",
				TLSCACert:     "test-ca-cert",
				TLSClientCert: "test-client-cert",
			},
			mockSetup:   nil,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mockSetup != nil {
				// Mock the NewClient function to return our mock client
				oldNewClient := mqttNewClient
				defer func() { mqttNewClient = oldNewClient }()

				mockClient := &MockClient{}
				mockToken := &MockToken{}
				tt.mockSetup(mockClient, mockToken)

				mqttNewClient = func(opts *mqtt.ClientOptions) mqtt.Client {
					return mockClient
				}
			}

			if tt.expectError {
				assert.Panics(t, func() { New(tt.cfg) })
			} else {
				assert.NotPanics(t, func() { New(tt.cfg) })
			}
		})
	}
}

func TestCreateTLSConfig(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Config
		expectError bool
	}{
		{
			name: "valid config",
			cfg: config.Config{
				TLSCACert:     validCACert,
				TLSClientCert: validClientCert,
				TLSClientKey:  validClientKey,
			},
			expectError: false,
		},
		{
			name: "missing CA cert",
			cfg: config.Config{
				TLSClientCert: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----",
				TLSClientKey:  "-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----",
			},
			expectError: true,
		},
		{
			name: "invalid CA cert",
			cfg: config.Config{
				TLSCACert:     "invalid-cert",
				TLSClientCert: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----",
				TLSClientKey:  "-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----",
			},
			expectError: true,
		},
		{
			name: "invalid client cert/key pair",
			cfg: config.Config{
				TLSCACert:     "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----",
				TLSClientCert: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----",
				TLSClientKey:  "invalid-key",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := createTLSConfig(tt.cfg)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
				assert.IsType(t, &tls.Config{}, config)
			}
		})
	}
}

func TestSubscribeAndListen(t *testing.T) {
	tests := []struct {
		name        string
		topic       string
		mockSetup   func(*MockClient, *MockToken)
		expectError bool
	}{
		{
			name:  "successful subscription",
			topic: "test/topic",
			mockSetup: func(mc *MockClient, mt *MockToken) {
				mt.On("Wait").Return(true)
				mt.On("Error").Return(nil)
				mc.On("Subscribe", "test/topic", byte(0), mock.AnythingOfType("mqtt.MessageHandler")).Return(mt)
			},
			expectError: false,
		},
		{
			name:  "subscription error",
			topic: "test/topic",
			mockSetup: func(mc *MockClient, mt *MockToken) {
				mt.On("Wait").Return(true)
				mt.On("Error").Return(errors.New("subscription failed"))
				mc.On("Subscribe", "test/topic", byte(0), mock.AnythingOfType("mqtt.MessageHandler")).Return(mt)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			mockToken := &MockToken{}
			tt.mockSetup(mockClient, mockToken)

			c := &Client{
				Client: mockClient,
			}

			handler := func(client mqtt.Client, msg mqtt.Message) {}
			err := c.SubscribeAndListen(tt.topic, handler)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockClient.AssertExpectations(t)
			mockToken.AssertExpectations(t)
		})
	}
}

// mqttNewClient is a variable that holds the mqtt.NewClient function
// This allows us to mock it in tests

const validCACert = `-----BEGIN CERTIFICATE-----
MIIDBTCCAe2gAwIBAgIUb2lvAqzZO7oZjfuc7/lcKDrHtKcwDQYJKoZIhvcNAQEL
BQAwEjEQMA4GA1UEAwwHdGVzdC1jYTAeFw0yNTA1MTQxMDA1MjBaFw0yNjA1MTQx
MDA1MjBaMBIxEDAOBgNVBAMMB3Rlc3QtY2EwggEiMA0GCSqGSIb3DQEBAQUAA4IB
DwAwggEKAoIBAQDR5+OL5al2ZL9ngFG//O4J8sVEaJHISxoN4KDkTNjkYSNwQSWW
ln+JZXArrVRBdt4LQjX54CgLibfBL1xD4G/RBzR2QuRZa9K4+i/y7fKUrcdf6Kow
fESLpUSNmhN+9Sk1yR2Xxs2Ka8Y1qzQNtX6sLTQfwWv96lZwuqw6UOuHwpfd1LG4
L2XOnjv5E8LDKhOThG3R/p+6UrjFkS/VRpsEjdyjW6R626jNSxPjqwLHeJCMfpv4
TkJzkhUIxd15CcfafcaGXrJI8tlElai/r3rndMT0XCG40TOBZT3OJ3UFyGnv+7qx
s76ubV4sPMay69vgslznO8d2b5dVBdkPq+L3AgMBAAGjUzBRMB0GA1UdDgQWBBTW
6Ypql+veZm9e8DlHXvjSqACHXDAfBgNVHSMEGDAWgBTW6Ypql+veZm9e8DlHXvjS
qACHXDAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQCSKj4qs8PE
31UOALX0F5150Nm6yfipoW6SnRSKfudUtuk+ATupukTgoUS5T0i7CyCV8SJETa0E
jv++7yWxDcLFZjEnY+1FbQCSlCUmmmdzVKQeOBt5rNOYOwPYq7H+NcNJDyhN79NK
aBnBWd+nTm52gLyKQSvC3Uwwk1VRqy+uVegDFsNfk7wBlGvDpa0OP7Wws4QZprKf
aE92P/AmwqF5H2GqpIfjD1K+WGubjUx1E77ZgP8zjK2mo8QTHbSJkdKS9N9QLj9W
QllHca7ue9e5TLgFQsQ+P4QhwlbknaDbXRXXpCXzDhV5UmMDFXtkWsgIBXcNmM9a
Ug1xlVNctHHE
-----END CERTIFICATE-----`

const validClientCert = `-----BEGIN CERTIFICATE-----
MIIDBTCCAe2gAwIBAgIUb2lvAqzZO7oZjfuc7/lcKDrHtKcwDQYJKoZIhvcNAQEL
BQAwEjEQMA4GA1UEAwwHdGVzdC1jYTAeFw0yNTA1MTQxMDA1MjBaFw0yNjA1MTQx
MDA1MjBaMBIxEDAOBgNVBAMMB3Rlc3QtY2EwggEiMA0GCSqGSIb3DQEBAQUAA4IB
DwAwggEKAoIBAQDR5+OL5al2ZL9ngFG//O4J8sVEaJHISxoN4KDkTNjkYSNwQSWW
ln+JZXArrVRBdt4LQjX54CgLibfBL1xD4G/RBzR2QuRZa9K4+i/y7fKUrcdf6Kow
fESLpUSNmhN+9Sk1yR2Xxs2Ka8Y1qzQNtX6sLTQfwWv96lZwuqw6UOuHwpfd1LG4
L2XOnjv5E8LDKhOThG3R/p+6UrjFkS/VRpsEjdyjW6R626jNSxPjqwLHeJCMfpv4
TkJzkhUIxd15CcfafcaGXrJI8tlElai/r3rndMT0XCG40TOBZT3OJ3UFyGnv+7qx
s76ubV4sPMay69vgslznO8d2b5dVBdkPq+L3AgMBAAGjUzBRMB0GA1UdDgQWBBTW
6Ypql+veZm9e8DlHXvjSqACHXDAfBgNVHSMEGDAWgBTW6Ypql+veZm9e8DlHXvjS
qACHXDAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQCSKj4qs8PE
31UOALX0F5150Nm6yfipoW6SnRSKfudUtuk+ATupukTgoUS5T0i7CyCV8SJETa0E
jv++7yWxDcLFZjEnY+1FbQCSlCUmmmdzVKQeOBt5rNOYOwPYq7H+NcNJDyhN79NK
aBnBWd+nTm52gLyKQSvC3Uwwk1VRqy+uVegDFsNfk7wBlGvDpa0OP7Wws4QZprKf
aE92P/AmwqF5H2GqpIfjD1K+WGubjUx1E77ZgP8zjK2mo8QTHbSJkdKS9N9QLj9W
QllHca7ue9e5TLgFQsQ+P4QhwlbknaDbXRXXpCXzDhV5UmMDFXtkWsgIBXcNmM9a
Ug1xlVNctHHE
-----END CERTIFICATE-----`

const validClientKey = `-----BEGIN PRIVATE KEY-----
MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQDR5+OL5al2ZL9n
gFG//O4J8sVEaJHISxoN4KDkTNjkYSNwQSWWln+JZXArrVRBdt4LQjX54CgLibfB
L1xD4G/RBzR2QuRZa9K4+i/y7fKUrcdf6KowfESLpUSNmhN+9Sk1yR2Xxs2Ka8Y1
qzQNtX6sLTQfwWv96lZwuqw6UOuHwpfd1LG4L2XOnjv5E8LDKhOThG3R/p+6UrjF
kS/VRpsEjdyjW6R626jNSxPjqwLHeJCMfpv4TkJzkhUIxd15CcfafcaGXrJI8tlE
lai/r3rndMT0XCG40TOBZT3OJ3UFyGnv+7qxs76ubV4sPMay69vgslznO8d2b5dV
BdkPq+L3AgMBAAECggEADPQ3HkCXozdXep84LFWDKTkCxJSBfq9n1bhppX06m2mF
Qt26YJ88ErIgaImjXADmdiJpa1jSj9e5b+Io2wWEUQ2VRsEdD4mwcPr7r43QvS02
UyxsKF7a6hVSdDywfFLL7sZRHbdGowbArjo5FamAPkbx4w3QSNTH7eAPVe/9gRy7
/iR/Ve5WpqwSsinbz7nQSxY59C2DhmiPO6d/klLtXrRyrNneDCot8j7MKxm9l7x+
vB8v47N6p6x2y7uZuGteX7XJc0inK69LYQhJfiUk95t3LJePyU4pTrOi23Vr1YRf
ViNOdiWFWN5GM4MuXM1BVhDoBEZtcfaS9SZmBYa1kQKBgQD+zc7RxRNb4Dk4wbJs
/lOLyOADM6wDdgXLBGy8KTa0GApNZIH3wriJ9M97qQA/OIMOr6aIMH1fguQO5Xx7
5b6hVoutKdS/yF3oHhsdHt+vfPToBVUYSNViU/catEyoPXcRotnjYjhZVvSfCMAM
ufPjkYyLtvDsSeOjmPEyjgq9JwKBgQDS5CC+yYNdMjvInv32UD06CffuyUVeZmlf
ClxsNubPjzIa+9lVPg6K1UVbB07ozzr57W64e74g3Y6RYE915F6wt4WJW+Y8SKmC
VIwr9feyDNNtZx3MpnP6I3b2kP3QzkEmhLqBGsH4m0D6hgvYo/RyxCqXOtWufntf
o9DZhw7tsQKBgQCpjJdXnHTCSRSqgLFSt3Uuac8uMj7+2pUGP35/QkllUy3fy8Zz
7/1NxzodBhrk9py2tAjzTJjQak+I3gmUhA7yWp18733i09gw8X+HRBkCM/rfPVUf
YK+ky0x9V4Y+2Q+XC69DEAOA50zFWlQ446+3OQ21llkAUjaIkOfGhR/+NwKBgE5I
HhuT465JgkWTNwQifse3gY/iqFxFOaHsz6ffrUeoiNnZWLA6q90/E1KZ4OGsYuD5
EJtsW4QJme0+yeAiGEASr3/wXANOmZVmWu3KjNpLxoOavkYEF5LnbTZTVdQXa7mn
lS9tRklJIBKehXEyUv/y7zhZv43ZJ2S2A0Vry8/RAoGBAPd9tGFmCdJwtZM2uoZn
ImicGWYc9V2eSf7ZQpgq6T42IxHaSyyUiy0dH0N3URTCEMM4FAiScpscLhwgSUGT
zNwktgK52RpxcZIJlzz5HEkwHndL2gqNwElsB9v3yY37zucior3o4QGpFXaDKFWM
1qjUpU8HqvNrQRDiegAwmBzp
-----END PRIVATE KEY-----`
