package tls

import (
	"crypto/tls"
	"log"
	"os"

	"github.com/joho/godotenv"

	"gprxy/internal/logger"
)

// Implements server side tls configuration for the proxy - It handles and configures tls for incoming connections from clients and when client connects and sends SSLRequest the proxy uses this config to upgrade connection to TLS

// Load loads TLS configuration from environment variables
// Returns nil if TLS is not configured (allowing proxy to run without TLS)
func Load() *tls.Config {
	err := godotenv.Load(".env")
	if err != nil {
		logger.Debug("no .env file found, using system environment")
	}

	proxyCert := os.Getenv("PROXY_CERT")
	proxyKey := os.Getenv("PROXY_KEY")

	// If TLS is not configured, return nil (proxy will work without TLS)
	if proxyCert == "" || proxyKey == "" {
		logger.Info("TLS not configured (PROXY_CERT or PROXY_KEY not set) - proxy will run without TLS support")
		return nil
	}

	// Load the certificate and private key
	cert, err := tls.LoadX509KeyPair(proxyCert, proxyKey)
	if err != nil {
		log.Fatalf("failed to load TLS certificate: %v", err)
	}

	// Create TLS config with security best practices
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12, // TLS 1.2 minimum (PostgreSQL standard)
		MaxVersion:   tls.VersionTLS13, // Allow TLS 1.3

		// Prefer server cipher suites for better security
		PreferServerCipherSuites: true,

		// Recommended cipher suites (modern, secure)
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
		},

		// Curve preferences (modern, secure curves)
		CurvePreferences: []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
			tls.CurveP384,
		},
	}

	logger.Info("TLS configured successfully (cert: %s, key: %s)", proxyCert, proxyKey)
	return config
}
