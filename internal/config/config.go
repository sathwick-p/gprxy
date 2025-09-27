package config

import (
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the proxy
type Config struct {
	Host        string
	Port        string
	ServiceUser string
	ServicePass string
}

// Load loads configuration from environment variables
func Load() *Config {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("No .env file found, using system environment")
	}

	host := os.Getenv("DB_HOST")
	if host == "" {
		host = "localhost"
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
		Host:        host,
		Port:        "7777",
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
		c.Host,
		database,
	)
}
