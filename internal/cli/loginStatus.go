package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		logger.Info("Token expired, attempting auto-refresh")

		// Try to refresh
		newCreds, err := getRefreshToken()
		if err != nil {
			logger.Error("Auto-refresh failed: %v", err)
			return false
		}

		logger.Info("Token auto-refreshed successfully")
		logger.Info("Already logged in as: %s (%s)", newCreds.UserInfo.Name, newCreds.UserInfo.Email)
		return true
	}
	logger.Info("Already logged in as: %s (%s)", creds.UserInfo.Name, creds.UserInfo.Email)
	logger.Info("Token expires in: %v", time.Until(creds.ExpiresAt).Round(time.Minute))
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

func getRefreshToken() (*SavedCreds, error) {
	logger.Info("loading existing creds")
	oldCreds, err := loadCreds()
	if err != nil {
		return nil, logger.Errorf("failed to load creds: %v", err)
	}

	if oldCreds.RefreshToken == "" {
		return nil, logger.Errorf("no refresh token found, pls authenticate using gprxy login")
	}

	auth0_url := os.Getenv("AUTH0_TENANT")
	client_id := os.Getenv("AUTH0_NATIVE_CLIENT_ID")
	if auth0_url == "" || client_id == "" {
		return nil, logger.Errorf("AUTH0_TENANT or AUTH0_NATIVE_CLIENT_ID not configured")
	}

	refresh_url, _ := url.Parse(fmt.Sprintf("https://%s/oauth/token", auth0_url))
	params := url.Values{}
	params.Add("grant_type", "refresh_token")
	params.Add("client_id", client_id)
	params.Add("refresh_token", oldCreds.RefreshToken)

	req, err := http.NewRequest("POST", refresh_url.String(), strings.NewReader(params.Encode()))

	if err != nil {
		return nil, logger.Errorf("failed to create refresh token request: %v", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := http.Client{
		Timeout: 30 * time.Second,
	}

	response, err := client.Do(req)
	if err != nil {
		return nil, logger.Errorf("failed to send refresh token request: %v", err)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, logger.Errorf("failed to read refresh token response: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, logger.Errorf("token refresh failed with status %d: %s", response.StatusCode, string(body))
	}

	var tokens TokenResponse

	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, logger.Errorf("failed to parse refresh token response: %v", err)
	}

	logger.Info("token refresh successful")

	var roles []string

	accessClaims, err := parseAccessToken(tokens.AccessToken)
	if err != nil {
		logger.Warn("failed to parse new access token: %v", err)
		roles = oldCreds.UserInfo.Roles
	} else {
		roles = extractRolesFromClaims(accessClaims)
	}

	newCreds := &SavedCreds{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second),
		IssuedAt:     time.Now(),
		UserInfo: UserInfo{
			Sub:   oldCreds.UserInfo.Sub,
			Email: oldCreds.UserInfo.Email,
			Name:  oldCreds.UserInfo.Name,
			Roles: roles},
	}

	if err := saveCreds(newCreds); err != nil {
		logger.Error("Failed to save refreshed credentials: %v", err)
		logger.Warn("New token obtained but not saved to disk")
	} else {
		logger.Info("Credentials updated and saved to ~/.gprxy/credentials")
	}

	logger.Info("New token expires at: %s", newCreds.ExpiresAt.Format(time.RFC1123))

	return newCreds, nil
}
