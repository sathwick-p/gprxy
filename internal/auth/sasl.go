package auth

import (
	"fmt"
	"log"

	"github.com/jackc/pgproto3/v2"
)

// handleSASLAuth handles SCRAM-SHA-256 authentication initial step
func handleSASLAuth(cb *pgproto3.Backend, bf *pgproto3.Frontend, clientAddr string) error {
	log.Printf("[%s] awaiting SASL initial response from client", clientAddr)
	clientMsg, err := cb.Receive()
	if err != nil {
		return fmt.Errorf("client disconnected during SASL: %w", err)
	}

	switch msg := clientMsg.(type) {
	case *pgproto3.SASLInitialResponse:
		log.Printf("[%s] client->backend: SASLInitialResponse (mechanism: %s)", clientAddr, msg.AuthMechanism)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send SASL response to backend: %w", err)
		}
	case *pgproto3.PasswordMessage:
		log.Printf("[%s] WARNING: client sent PasswordMessage for SASL auth (protocol mismatch)", clientAddr)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send password to backend: %w", err)
		}
	default:
		return fmt.Errorf("unexpected client message for SASL: %T", clientMsg)
	}
	return nil
}

// handleSASLContinue handles SCRAM-SHA-256 authentication continue step
func handleSASLContinue(cb *pgproto3.Backend, bf *pgproto3.Frontend, clientAddr string) error {
	log.Printf("[%s] awaiting SASL response from client", clientAddr)
	clientMsg, err := cb.Receive()
	if err != nil {
		return fmt.Errorf("client disconnected during SASL continue: %w", err)
	}

	switch msg := clientMsg.(type) {
	case *pgproto3.SASLResponse:
		log.Printf("[%s] client->backend: SASLResponse", clientAddr)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send SASL response to backend: %w", err)
		}
	default:
		return fmt.Errorf("unexpected client message for SASL continue: %T (expected SASLResponse)", msg)
	}
	return nil
}
