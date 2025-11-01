package config

import (
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the proxy
type Config struct {
	ProxyHost   string // Proxy listen address
	ProxyPort   string // Proxy listen port
	DBHost      string // PostgreSQL database host
	ServiceUser string
	ServicePass string
}

// Load loads configuration from environment variables
func Load() *Config {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("No .env file found, using system environment")
	}

	// Proxy listen configuration
	proxyHost := os.Getenv("PROXY_HOST")
	if proxyHost == "" {
		proxyHost = "0.0.0.0" // Listen on all interfaces by default
	}

	proxyPort := os.Getenv("PROXY_PORT")
	if proxyPort == "" {
		proxyPort = "7777"
	}

	// PostgreSQL database host
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}

	serviceUser := os.Getenv("GPRXY_USER")
	servicePass := os.Getenv("GPRXY_PASS")

	if serviceUser == "" {
		log.Fatal("GPRXY_USER environment variable is required")
	}
	if servicePass == "" {
		log.Fatal("GPRXY_PASS environment variable is required")
	}

	return &Config{
		ProxyHost:   proxyHost,
		ProxyPort:   proxyPort,
		DBHost:      dbHost,
		ServiceUser: serviceUser,
		ServicePass: servicePass,
	}
}

// BuildConnectionString creates a PostgreSQL connection string for a specific database
func (c *Config) BuildConnectionString(database string) string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:5432/%s",
		c.ServiceUser,
		c.ServicePass,
		c.DBHost,
		database,
	)
}
