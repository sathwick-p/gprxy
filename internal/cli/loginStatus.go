package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gprxy.com/internal/logger"
)

type SavedCreds struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	IssuedAt     time.Time `json:"issued_at"`
	UserInfo     UserInfo  `json:"user_info"`
}
type UserInfo struct {
	Sub   string   `json:"sub"`
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

func getCredentialsPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	gprxyDir := filepath.Join(homeDir, ".gprxy")
	if err := os.MkdirAll(gprxyDir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(gprxyDir, "credentials"), nil
}

func saveCreds(creds *SavedCreds) error {
	path, err := getCredentialsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func loadCreds() (*SavedCreds, error) {
	path, err := getCredentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds SavedCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}
func isExpired(creds *SavedCreds) bool {
	return time.Until(creds.ExpiresAt) < 3*time.Minute
}
func loginStatus() bool {
	creds, err := loadCreds()
	if err != nil {
		logger.Error("Unable to load creds", err)
		return false
	}
	if isExpired(creds) {
		// Token expired, must authenticate
		return false
	}
	logger.Printf("Already logged in as: %s (%s)", creds.UserInfo.Name, creds.UserInfo.Email)
	return true

}

func parseAccessToken(at string) (map[string]interface{}, error) {
	parse := strings.Split(at, ".")

	if len(parse) != 3 {
		return nil, errors.New("invalid token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parse[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil

}

func parseIDToken(idToken string) (*UserInfo, error) {
	parse := strings.Split(idToken, ".")
	if len(parse) != 3 {
		return nil, errors.New("invalid token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parse[1])
	if err != nil {
		return nil, err
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}

	info := &UserInfo{
		Sub:   getString(claims, "sub"),
		Email: getString(claims, "email"),
		Name:  getString(claims, "name"),
	}
	return info, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractRolesFromClaims(claims map[string]interface{}) []string {
	roleInterface, ok := claims["role"]
	if !ok {
		return []string{}
	}

	rolesArr, ok := roleInterface.([]interface{})
	if !ok {
		return []string{}
	}

	roles := make([]string, 0, len(rolesArr))
	for _, r := range rolesArr {
		if roleStr, ok := r.(string); ok {
			roles = append(roles, roleStr)
		}
	}

	return roles
}
