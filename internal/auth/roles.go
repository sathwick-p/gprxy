package auth

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gprxy/internal/logger"
)

// RoleMapper maps OAuth roles to PostgreSQL service accounts
type RoleMapper struct {
	roleToAccount map[string]ServiceAccount
	defaultRole   string
	mu            sync.RWMutex
}

// ServiceAccount represents a PostgreSQL service account
type ServiceAccount struct {
	Username string
	Password string
	Role     string
}

// NewRoleMapper creates a new role mapper with environment-based configuration
func NewRoleMapper() (*RoleMapper, error) {
	mapper := &RoleMapper{
		roleToAccount: make(map[string]ServiceAccount),
		defaultRole:   os.Getenv("DEFAULT_ROLE"),
	}

	// Load role mappings from environment variables

	if err := mapper.loadFromEnvironment(); err != nil {
		return nil, err
	}

	if len(mapper.roleToAccount) == 0 {
		return nil, fmt.Errorf("no role mappings configured")
	}

	return mapper, nil
}

// loadFromEnvironment loads role mappings from environment variables
func (rm *RoleMapper) loadFromEnvironment() error {
	envVars := os.Environ()
	loaded := 0

	for _, env := range envVars {
		// Look for ROLE_MAPPING_ prefix
		if !strings.HasPrefix(env, "ROLE_MAPPING_") {
			continue
		}

		// Parse environment variable
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		// Extract role name (lowercase)
		roleKey := strings.TrimPrefix(parts[0], "ROLE_MAPPING_")
		role := strings.ToLower(roleKey)

		// Parse username:password
		credentials := strings.SplitN(parts[1], ":", 2)
		if len(credentials) != 2 {
			logger.Warn("Invalid role mapping format for %s (expected username:password)", roleKey)
			continue
		}

		username := strings.TrimSpace(credentials[0])
		password := strings.TrimSpace(credentials[1])

		if username == "" || password == "" {
			logger.Warn("Empty username or password for role %s", role)
			continue
		}

		rm.roleToAccount[role] = ServiceAccount{
			Username: username,
			Password: password,
			Role:     role,
		}

		loaded++
		logger.Info("Loaded role mapping: %s → %s", role, username)
	}

	if loaded == 0 {
		return fmt.Errorf("no valid role mappings found (set ROLE_MAPPING_<ROLE>=username:password)")
	}

	return nil
}

// MapRoleToServiceAccount maps user roles to a PostgreSQL service account
func (rm *RoleMapper) MapRoleToServiceAccount(roles []string) (*ServiceAccount, error) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	if len(roles) == 0 {
		return rm.handleNoRoles()
	}

	// Try each role in order (first match wins)
	for _, role := range roles {
		normalizedRole := strings.ToLower(strings.TrimSpace(role))
		if account, exists := rm.roleToAccount[normalizedRole]; exists {
			logger.Debug("Mapped role '%s' to service account '%s'", role, account.Username)
			return &account, nil
		}
	}

	// No matching role found
	logger.Warn("No service account found for roles: %v", roles)
	return rm.handleNoRoles()
}

// handleNoRoles returns the default role's service account or an error
func (rm *RoleMapper) handleNoRoles() (*ServiceAccount, error) {
	if rm.defaultRole == "" {
		return nil, fmt.Errorf("user has no valid roles and no default role configured")
	}

	account, exists := rm.roleToAccount[rm.defaultRole]
	if !exists {
		return nil, fmt.Errorf("default role '%s' not found in role mappings", rm.defaultRole)
	}

	logger.Debug("Using default service account '%s'", account.Username)
	return &account, nil
}

// AddRoleMapping adds or updates a role mapping (useful for testing or dynamic config)
func (rm *RoleMapper) AddRoleMapping(role, username, password string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	rm.roleToAccount[normalizedRole] = ServiceAccount{
		Username: username,
		Password: password,
		Role:     normalizedRole,
	}

	logger.Debug("Added role mapping: %s → %s", normalizedRole, username)
}

// GetAllRoles returns all configured roles (useful for diagnostics)
func (rm *RoleMapper) GetAllRoles() []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	roles := make([]string, 0, len(rm.roleToAccount))
	for role := range rm.roleToAccount {
		roles = append(roles, role)
	}
	return roles
}
