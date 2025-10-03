package proxy

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"

	"gprxy.com/internal/config"
)

// Server represents the proxy server
type Server struct {
	config    *config.Config
	tlsConfig *tls.Config
}

// NewServer creates a new proxy server
func NewServer(cfg *config.Config, tls *tls.Config) *Server {
	return &Server{
		config:    cfg,
		tlsConfig: tls,
	}
}

// Start starts the proxy server and listens for client connections
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.config.Host+":"+s.config.Port)
	if err != nil {
		return fmt.Errorf("failed to start proxy server: %w", err)
	}

	tlsStatus := "disabled"
	if s.tlsConfig != nil {
		tlsStatus = "enabled"
	}
	log.Printf("PostgreSQL proxy listening on %s (TLS: %s)", ln.Addr(), tlsStatus)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("failed to accept connection: %v", err)
			continue
		}

		pc := &Connection{
			conn:      conn,
			config:    s.config,
			tlsConfig: s.tlsConfig,
		}
		go pc.handleConnection()
	}
}
