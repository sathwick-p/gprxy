package pool

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	poolManager = make(map[string]*pgxpool.Pool)
	poolMutex   sync.RWMutex
)

// GetOrCreatePool returns an existing pool or creates a new one for the given database
func GetOrCreatePool(database, connectionString string) (*pgxpool.Pool, error) {
	const defaultMaxConns = int32(7)
	const defaultMinConns = int32(0)
	const defaultMaxConnLifetime = time.Hour
	const defaultMaxConnIdleTime = time.Minute * 30
	const defaultHealthCheckPeriod = time.Minute
	const defaultConnectTimeout = time.Second * 5

	poolMutex.RLock() // read lock for go routines trying to read the pool
	pool, exists := poolManager[database]
	poolMutex.RUnlock()

	if exists {
		return pool, nil
	}

	poolMutex.Lock()
	defer poolMutex.Unlock()

	// checking to see if it exists (double-check pattern)
	if pool, exists := poolManager[database]; exists {
		return pool, nil
	}

	// else if it does not exist then create it
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	config.MaxConns = defaultMaxConns
	config.MinConns = defaultMinConns
	config.MaxConnLifetime = defaultMaxConnLifetime
	config.MaxConnIdleTime = defaultMaxConnIdleTime
	config.HealthCheckPeriod = defaultHealthCheckPeriod
	config.ConnConfig.ConnectTimeout = defaultConnectTimeout

	pool, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to create pool: %w", err)
	}
	poolManager[database] = pool
	log.Printf("Created a new connection pool for database: %s", database)
	return pool, nil
}

// AcquireConnection acquires a connection from the pool for the given database
func AcquireConnection(database, connectionString string) (*pgxpool.Conn, error) {
	pool, err := GetOrCreatePool(database, connectionString)
	if err != nil {
		return nil, fmt.Errorf("error while creating connection to the database: %w", err)
	}

	// Acquire a connection from the pool
	connection, err := pool.Acquire(context.Background())
	if err != nil {
		return nil, fmt.Errorf("error while acquiring connection from the database pool: %w", err)
	}

	// Test connection
	err = connection.Ping(context.Background())
	if err != nil {
		connection.Release() // Release on error
		return nil, fmt.Errorf("could not ping database: %w", err)
	}

	return connection, nil
}

// LogPoolStats logs statistics for the given database pool
func LogPoolStats(database string) {
	poolMutex.RLock()
	pool, exists := poolManager[database]
	poolMutex.RUnlock()

	if !exists {
		log.Printf("No pool found for database: %s", database)
		return
	}

	stats := pool.Stat()
	log.Printf("Pool stats - Total: %d, Acquired: %d, Idle: %d",
		stats.TotalConns(), stats.AcquiredConns(), stats.IdleConns())
}
