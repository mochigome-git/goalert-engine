package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	MQTTBroker    string
	MQTTTopic     string
	MQTTUsername  string
	MQTTPassword  string
	MQTTClientID  string
	SupabaseURL   string
	SupabaseKey   string
	Schema        string
	TLSCACert     string
	TLSClientCert string
	TLSClientKey  string

	Supabase struct {
		URL             string
		Key             string
		Table           string
		Schema          string
		ForeignKey      string
		ForeignKeyCheck string
		Realtime        string
	}
}

func Load() Config {
	// Try multiple locations for .env.local so it works whether you run
	// via `go run ./...` (cwd = project root) or as a compiled binary
	// (cwd = wherever the binary is).
	candidates := []string{
		".env.local",
		".env",
	}

	// Also try the directory of the source file (dev only)
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Dir(filename)
		candidates = append(candidates,
			filepath.Join(dir, "../../.env.local"),
			filepath.Join(dir, "../../.env"),
		)
	}

	loaded := false
	for _, path := range candidates {
		if err := godotenv.Load(path); err == nil {
			fmt.Printf("Info: loaded env from %s\n", path)
			loaded = true
			break
		}
	}
	if !loaded {
		fmt.Println("Info: no .env.local or .env found — using system environment variables")
	}

	schema := getenv("SUPABASE_SCHEMA", "alerts")

	cfg := Config{
		MQTTBroker:    getenv("MQTT_BROKER", ""),
		MQTTTopic:     getenv("MQTT_TOPIC", "#"),
		MQTTUsername:  getenv("MQTT_USERNAME", ""),
		MQTTPassword:  getenv("MQTT_PASSWORD", ""),
		MQTTClientID:  getenv("MQTT_CLIENT_ID", ""),
		SupabaseURL:   getenv("SUPABASE_URL", ""),
		SupabaseKey:   getenv("SUPABASE_KEY", ""),
		Schema:        schema,
		TLSCACert:     loadCert("TLS_CA_CERT"),
		TLSClientCert: loadCert("TLS_CLIENT_CERT"),
		TLSClientKey:  loadCert("TLS_CLIENT_KEY"),
	}

	cfg.Supabase.URL = cfg.SupabaseURL
	cfg.Supabase.Key = cfg.SupabaseKey
	cfg.Supabase.Table = getenv("SUPABASE_RULES_TABLE", "alert_rules")
	cfg.Supabase.Schema = schema
	cfg.Supabase.ForeignKey = getenv("SUPABASE_RULES_FK", "*")
	cfg.Supabase.ForeignKeyCheck = getenv("SUPABASE_RULES_FK_EQ", "")
	cfg.Supabase.Realtime = getenv("SUPABASE_REALTIME_TABLE", "model_group")

	return cfg
}

// getenv reads an env var and strips surrounding quotes so both
// quoted (.env.local style) and unquoted (system env) values work.
func getenv(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') ||
			(v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
	}
	return v
}

// loadCert supports two formats:
//
//	File path   — TLS_CA_CERT=/path/to/ca.pem   (recommended)
//	Inline PEM  — TLS_CA_CERT=-----BEGIN CERTIFICATE-----\n...
func loadCert(key string) string {
	v := getenv(key, "")
	if v == "" {
		return ""
	}
	// Looks like a file path (no PEM header, no newlines)
	if !strings.Contains(v, "BEGIN") && !strings.Contains(v, "\n") {
		data, err := os.ReadFile(v)
		if err == nil {
			return string(data)
		}
		fmt.Printf("Warning: %s value %q looks like a path but could not be read: %v\n", key, v, err)
	}
	return v
}
