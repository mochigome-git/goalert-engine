package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	MQTTBroker    string
	MQTTTopic     string
	SupabaseURL   string // Supabase API endpoint's URL
	SupabaseKey   string // Supabase Service Role Key
	Schema        string // Supabase Custom Schema
	TLSCACert     string // TLS CA certificate as a string (PEM format)
	TLSClientCert string // Client certificate as a string (PEM format)
	TLSClientKey  string // Client private key as a string (PEM format)

	Supabase struct {
		URL    string
		Key    string
		Table  string
		Schema string
	}
}

func Load() Config {
	// Optional fallback: try to load .env.local
	if err := godotenv.Load(".env.local"); err != nil {
		fmt.Println("Info: .env.local not found, using system environment variables")
	}

	return Config{
		MQTTBroker:    os.Getenv("MQTT_BROKER"),
		MQTTTopic:     os.Getenv("MQTT_TOPIC"),
		SupabaseURL:   os.Getenv("SUPABASE_URL"),
		SupabaseKey:   os.Getenv("SUPABASE_KEY"),
		Schema:        os.Getenv("SUPABASE_SCHEMA"),
		TLSCACert:     os.Getenv("TLS_CA_CERT"),
		TLSClientCert: os.Getenv("TLS_CLIENT_CERT"),
		TLSClientKey:  os.Getenv("TLS_CLIENT_KEY"),
		Supabase: struct {
			URL    string
			Key    string
			Table  string
			Schema string
		}{
			URL:    os.Getenv("SUPABASE_URL"),
			Key:    os.Getenv("SUPABASE_KEY"),
			Table:  os.Getenv("SUPABASE_RULES_TABLE"),
			Schema: os.Getenv("SUPABASE_SCHEMA"),
		},
	}
}
