package pool

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gprxy.com/internal/logger"
)

type poolKey struct {
	user     string
	database string
}

var (
	poolManager = make(map[poolKey]*pgxpool.Pool)
	poolMutex   sync.RWMutex
)

// GetOrCreatePool returns an existing pool or creates a new one for the given database
func GetOrCreatePool(user, database, connectionString string) (*pgxpool.Pool, error) {
	const defaultMaxConns = int32(5)
	const defaultMinConns = int32(0)
	const defaultMaxConnLifetime = time.Hour
	const defaultMaxConnIdleTime = time.Minute * 30
	const defaultHealthCheckPeriod = time.Minute
	const defaultConnectTimeout = time.Second * 5

	key := poolKey{
		user:     user,
		database: database,
	}
	poolMutex.RLock()
	pool, exists := poolManager[key]
	poolMutex.RUnlock()

	if exists {
		return pool, nil
	}

	poolMutex.Lock()
	defer poolMutex.Unlock()

	if pool, exists := poolManager[key]; exists {
		return pool, nil
	}

	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, logger.Errorf("failed to parse config: %w", err)
	}

	config.MaxConns = defaultMaxConns
	config.MinConns = defaultMinConns
	config.MaxConnLifetime = defaultMaxConnLifetime
	config.MaxConnIdleTime = defaultMaxConnIdleTime
	config.HealthCheckPeriod = defaultHealthCheckPeriod
	config.ConnConfig.ConnectTimeout = defaultConnectTimeout

	pool, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, logger.Errorf("failed to create pool: %w", err)
	}
	poolManager[key] = pool
	logger.Info("created connection pool for database: %s", database)
	return pool, nil
}

// AcquireConnection acquires a connection from the pool for the given database and user
func AcquireConnection(user, database, connectionString string) (*pgxpool.Conn, error) {
	pool, err := GetOrCreatePool(user, database, connectionString)
	if err != nil {
		return nil, logger.Errorf("error while creating connection to the database: %w", err)
	}

	connection, err := pool.Acquire(context.Background())
	if err != nil {
		return nil, logger.Errorf("error while acquiring connection from the database pool: %w", err)
	}

	err = connection.Ping(context.Background())
	if err != nil {
		connection.Release()
		return nil, logger.Errorf("could not ping database: %w", err)
	}

	return connection, nil
}
