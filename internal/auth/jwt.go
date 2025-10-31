package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"

	"math/big"
	"net/http"
	"sync"
	"time"

	"gprxy/internal/logger"

	"github.com/golang-jwt/jwt/v5"
)

type JWTValidator struct {
	issuer        string
	audience      string
	jwksURL       string
	publicKeys    map[string]*rsa.PublicKey
	keysMutex     sync.RWMutex
	lastKeysFetch time.Time
	keysCacheTTL  time.Duration
	httpClient    *http.Client
}

type OAuthContext struct {
	Email          string
	Roles          []string
	Subject        string
	ServiceAccount string
	ExpiresAt      time.Time
	IssuedAt       time.Time
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Alg string `json:"alg"`
}

func NewJWTValidator(issuer, audience, jwksURL string) *JWTValidator {
	return &JWTValidator{
		issuer:       issuer,
		audience:     audience,
		jwksURL:      jwksURL,
		publicKeys:   make(map[string]*rsa.PublicKey),
		keysCacheTTL: 1 * time.Hour,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (v *JWTValidator) ValidateJWT(authToken string) (*OAuthContext, error) {
	token, err := jwt.Parse(authToken, func(t *jwt.Token) (interface{}, error) {

		// parse and validate the algo
		if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, logger.Errorf("unexpected signing method: %v ", t.Header["alg"])
		}

		// get key id from token header

		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, logger.Errorf("key id (kid) not found in token header")
		}

		// get pub key
		publicKey, err := v.getPublicKey(kid)
		if err != nil {
			return nil, logger.Errorf("failed to get public key: %v", err)
		}

		return publicKey, nil

	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}))

	if err != nil {
		return nil, logger.Errorf("jwt validation failed:%v", err)
	}

	if !token.Valid {
		return nil, logger.Errorf("jwt token invalid: %v", err)
	}

	// Extract claims

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, logger.Errorf("failed to parse jwt claims")
	}

	// validate issuer

	iss, ok := claims["iss"].(string)

	if !ok || iss != v.issuer {
		return nil, logger.Errorf("invalid issue: %v", err)
	}

	// validate audience
	if err := v.validateAudience(claims); err != nil {
		return nil, err
	}

	// get user info
	oauthContext := &OAuthContext{}

	// email

	email, ok := claims["email"].(string)
	if !ok || email == "" {
		return nil, logger.Errorf("email not found in jwt")
	}

	oauthContext.Email = email

	// subject

	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return nil, logger.Errorf("sub claim not found in jwt")
	}
	oauthContext.Subject = sub

	// Roles

	oauthContext.Roles = v.extractRoles(claims)

	// Expiration
	if exp, ok := claims["exp"].(float64); ok {
		oauthContext.ExpiresAt = time.Unix(int64(exp), 0)
	}

	// Issued at
	if iat, ok := claims["iat"].(float64); ok {
		oauthContext.IssuedAt = time.Unix(int64(iat), 0)
	}

	// Validate expiration
	if time.Now().After(oauthContext.ExpiresAt) {
		return nil, logger.Errorf("JWT token has expired")
	}

	logger.Debug("JWT validated successfully for user: %s (roles: %v)", oauthContext.Email, oauthContext.Roles)
	return oauthContext, nil

}
func (v *JWTValidator) extractRoles(claims jwt.MapClaims) []string {
	roles := []string{}

	// Try "role" claim
	if rolesClaim, ok := claims["role"]; ok {
		switch v := rolesClaim.(type) {
		case []interface{}:
			for _, r := range v {
				if roleStr, ok := r.(string); ok {
					roles = append(roles, roleStr)
				}
			}
		case string:
			roles = append(roles, v)
		}
	}

	// Try "roles" claim
	if rolesClaim, ok := claims["roles"]; ok {
		switch v := rolesClaim.(type) {
		case []interface{}:
			for _, r := range v {
				if roleStr, ok := r.(string); ok {
					roles = append(roles, roleStr)
				}
			}
		case string:
			roles = append(roles, v)
		}
	}

	return roles
}
func (v *JWTValidator) validateAudience(claims jwt.MapClaims) error {
	aud, ok := claims["aud"]
	if !ok {
		return logger.Errorf("aud claim not found in jwt")
	}

	switch audience := aud.(type) {
	case string:
		if audience != v.audience {
			return logger.Errorf("invalid audience expected: %v, got: %v", v.audience, audience)
		}

	case []interface{}:
		found := false
		for _, a := range audience {
			if audStr, ok := a.(string); ok && audStr == v.audience {
				found = true
				break
			}

		}

		if !found {
			return logger.Errorf("invalid audience: %s not found in audience list", v.audience)
		}
	default:
		return logger.Errorf("invalid audience type: %v", audience)
	}
	return nil
}

func (v *JWTValidator) getPublicKey(kid string) (*rsa.PublicKey, error) {
	v.keysMutex.RLock()

	if key, exists := v.publicKeys[kid]; exists {
		// check if still valid

		if time.Since(v.lastKeysFetch) < v.keysCacheTTL {
			v.keysMutex.RUnlock()
			return key, nil
		}
	}

	v.keysMutex.RUnlock()

	// fetch new keys
	v.keysMutex.Lock()
	defer v.keysMutex.Unlock()

	if key, exists := v.publicKeys[kid]; exists && time.Since(v.lastKeysFetch) < v.keysCacheTTL {
		return key, nil
	}

	logger.Debug("fetching jwks from %s", v.jwksURL)
	err := v.fetchJWKS()
	if err != nil {
		return nil, err
	}
	key, exists := v.publicKeys[kid]
	if !exists {
		return nil, logger.Errorf("public key with kid %s not found in JWKS", kid)
	}

	return key, nil
}

func (v *JWTValidator) fetchJWKS() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", v.jwksURL, nil)
	if err != nil {
		return logger.Errorf("failed to create JWKS request: %v", err)
	}

	response, err := v.httpClient.Do(req)

	if err != nil {
		return logger.Errorf("failed to fetch JWKS: %w", err)
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return logger.Errorf("jwks endpoint returned %v", response.StatusCode)
	}

	var jwks JWKS
	if err := json.NewDecoder(response.Body).Decode(&jwks); err != nil {
		return logger.Errorf("failed to decode jwks: %v", err)
	}

	// Parse Keys

	for _, jwk := range jwks.Keys {
		if jwk.Kty != "RSA" || jwk.Use != "sig" {
			continue
		}

		// parsing the public key
		// decoding modulus - The RSA modulus, base64url-encoded. One half of the RSA public key.
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil {
			return logger.Errorf("failed to decode n: %v", err)
		}

		// decoding exponent (e)
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil {
			return logger.Errorf("failed to decode e: %v", err)
		}

		pubKey := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: 0,
		}

		// convert exponent bytes to int

		for _, b := range eBytes {
			pubKey.E = pubKey.E<<8 + int(b)
		}

		v.publicKeys[jwk.Kid] = pubKey

		logger.Debug("loaded public key: kid=%s, alg=%s", jwk.Kid, jwk.Alg)

	}
	v.lastKeysFetch = time.Now()

	logger.Info("loaded %d public keys from JWKS", len(v.publicKeys))

	return nil
}
