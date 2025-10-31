package proxy

import (
	"crypto/tls"
	"net"
	"sync"

	"gprxy/internal/config"
	"gprxy/internal/logger"
)

// Server represents the proxy server
type Server struct {
	config            *config.Config
	tlsConfig         *tls.Config
	activeConnections map[uint64]*Connection
	connMutex         sync.RWMutex
}

// Combines ProcessID and SecretKey into a single uint64:
// high 32 bits = ProcessID, low 32 bits = SecretKey
func (s *Server) makeCancelKey(processId, secretKey uint32) uint64 {
	return (uint64(processId) << 32) | uint64(secretKey)
}

func (s *Server) registerConnection(processId, secretkey uint32, conn *Connection) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()
	key := s.makeCancelKey(processId, secretkey)
	s.activeConnections[key] = conn
	logger.Debug("registered connection: PID=%d, secret_key=%d, map_key=%d", processId, secretkey, key)
	logger.Debug("active connections in registry: %d", len(s.activeConnections))
	for k, v := range s.activeConnections {
		logger.Debug("registry entry: key=%d, user=%s, db=%s, secret_key=%v, pid=%v",
			k, v.user, v.db, v.poolConn.Conn().PgConn().SecretKey(), v.poolConn.Conn().PgConn().PID())
	}
}

func (s *Server) unregisterConnection(processId, secretkey uint32, conn *Connection) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()
	key := s.makeCancelKey(processId, secretkey)
	delete(s.activeConnections, key)
}

func (s *Server) getConnectionForCancelRequest(processId, secretkey uint32) (*Connection, bool) {
	s.connMutex.RLock()
	defer s.connMutex.RUnlock()
	key := s.makeCancelKey(processId, secretkey)

	logger.Debug("looking for cancel key: PID=%d, secret_key=%d, computed_key=%d",
		processId, secretkey, key)
	logger.Debug("active connections in registry: %d", len(s.activeConnections))

	for k, v := range s.activeConnections {
		logger.Debug("registry entry: key=%d, user=%s, db=%s", k, v.user, v.db)
	}

	conn, exists := s.activeConnections[key]
	if exists {
		logger.Debug("found connection for cancel request: user=%s, db=%s", conn.user, conn.db)
	} else {
		logger.Debug("connection not found for cancel key=%d", key)
	}
	return conn, exists
}

// NewServer creates a new proxy server
func NewServer(cfg *config.Config, tls *tls.Config) *Server {
	return &Server{
		config:            cfg,
		tlsConfig:         tls,
		activeConnections: make(map[uint64]*Connection),
	}
}

// Start starts the proxy server and listens for client connections
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.config.Host+":"+s.config.Port)
	if err != nil {
		return logger.Errorf("failed to start proxy server: %w", err)
	}

	tlsStatus := "disabled"
	if s.tlsConfig != nil {
		tlsStatus = "enabled"
	}
	logger.Info("PostgreSQL proxy listening on %s (TLS: %s)", ln.Addr(), tlsStatus)

	for {
		conn, err := ln.Accept()
		if err != nil {
			logger.Error("failed to accept connection: %v", err)
			continue
		}

		pc := &Connection{
			conn:      conn,
			config:    s.config,
			tlsConfig: s.tlsConfig,
			server:    s,
		}
		go pc.handleConnection()
	}
}
