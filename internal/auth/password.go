package auth

import (
	"fmt"
	"log"

	"github.com/jackc/pgproto3/v2"
)

// handlePasswordAuth handles MD5 or cleartext password authentication
func handlePasswordAuth(cb *pgproto3.Backend, bf *pgproto3.Frontend, clientAddr string, authMsg pgproto3.BackendMessage) error {
	log.Printf("[%s] awaiting password from client", clientAddr)
	clientMsg, err := cb.Receive()
	if err != nil {
		return fmt.Errorf("client disconnected during password auth: %w", err)
	}

	switch msg := clientMsg.(type) {
	case *pgproto3.PasswordMessage:
		log.Printf("[%s] client->backend: PasswordMessage", clientAddr)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send password to backend: %w", err)
		}
	default:
		return fmt.Errorf("unexpected client message for password auth: %T (expected PasswordMessage)", msg)
	}
	return nil
}
