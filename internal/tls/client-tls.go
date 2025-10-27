// This implements the Client-side TLS for CLI connecting to the proxy.

package tls

import (
	"crypto/tls"
	"encoding/binary"
	"net"

	"gprxy.com/internal/logger"
)

func UpgradeToTLS(conn net.Conn, serverName string) (net.Conn, error) {
	sslreq := make([]byte, 8)
	binary.BigEndian.PutUint32(sslreq[0:4], 8)        //length
	binary.BigEndian.PutUint32(sslreq[4:8], 80877103) // ssl code

	if _, err := conn.Write(sslreq); err != nil {
		return nil, logger.Errorf("failed to send SSL request: %w", err)
	}

	response := make([]byte, 1) //read server response for ssl 'S' or 'N'
	if _, err := conn.Read(response); err != nil {
		return nil, logger.Errorf("failed to read SSL response: %w", err)
	}

	if response[0] == 'N' {
		logger.Warn("proxy does  not support TLS")
		return conn, nil
	}

	tlsConfig := &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	}

	tlsconn := tls.Client(conn, tlsConfig)
	if err := tlsconn.Handshake(); err != nil {
		return nil, logger.Errorf("tls handshake failed: %w", err)
	}

	state := tlsconn.ConnectionState()
	version := "1.2"
	if state.Version == tls.VersionTLS13 {
		version = "1.3"
	}
	logger.Info("tls connection established (TLS %s, cipher: %s)",
		version, tls.CipherSuiteName(state.CipherSuite))
	return tlsconn, nil

}
