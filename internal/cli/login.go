package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"

	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"io"

	"gprxy/internal/logger"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

func init() {
	godotenv.Load(".env")
	rootCommand.AddCommand(oidc_login)
}

var oidc_login = &cobra.Command{
	Use:   "login",
	Short: "Perform OIDC token based login via cli",
	Long: `Authenticate using single sign-on and save credentials locally.

After successful authentication, use 'gprxy connect' to connect to databases.
Tokens are cached and automatically refreshed when needed.

Your credentials are stored securely in ~/.gprxy/credentials
`,
	Run: login,
}

type OAuthSession struct {
	state        string
	codeVerifier string
	mu           sync.Mutex
}
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func generateCodeVerifier(count int) (string, error) {
	buf := make([]byte, count)
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		logger.Error("unable to generate string for pkce: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generateCodeChallenge(code_verifier string) string {
	sha2 := sha256.New()
	io.WriteString(sha2, code_verifier)
	code_challenge := base64.RawURLEncoding.EncodeToString(sha2.Sum(nil))
	return code_challenge
}
func buildAuthURL(code_verifier, code_challenge, state string) string {
	tenant_url := os.Getenv("AUTH0_TENANT")

	client_id := os.Getenv("AUTH0_NATIVE_CLIENT_ID")
	if tenant_url == "" || client_id == "" {
		logger.Fatal("auth0 config missing for authentication")
	}
	callbackurl := os.Getenv("CALLBACK_URL")
	if callbackurl == "" {
		logger.Error("callback url missing for authentication")
	}
	audience := os.Getenv("AUDIENCE")
	if audience == "" {
		logger.Error("audience parameter missing for authentication")
	}
	connection_name := os.Getenv("CONNECTION_NAME")
	auth_url, _ := url.Parse(fmt.Sprintf("https://%s/authorize", tenant_url))
	params := url.Values{}
	params.Add("response_type", "code")
	params.Add("code_challenge", code_challenge)
	params.Add("code_challenge_method", "S256")
	params.Add("client_id", client_id)
	params.Add("redirect_uri", callbackurl)
	params.Add("scope", "openid profile email offline_access groups")
	params.Add("audience", audience)
	params.Add("state", state)
	params.Add("connection", connection_name)
	auth_url.RawQuery = params.Encode()
	return auth_url.String()

}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	default:
		if isWSL() {
			cmd = "cmd.exe"
			args = []string{"/c", "start", url}
		} else {
			cmd = "xdg-open"
			args = []string{url}
		}
	}
	if len(args) > 1 {
		// args[0] is used for 'start' command argument, to prevent issues with URLs starting with a quote
		args = append(args[:1], append([]string{""}, args[1:]...)...)
	}
	return exec.Command(cmd, args...).Start()

}

// isWSL checks if the Go program is running inside Windows Subsystem for Linux
func isWSL() bool {
	releaseData, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(releaseData)), "microsoft")
}

func startCallbackServer(ctx context.Context, session *OAuthSession) (string, error) {
	codeChan := make(chan string)
	errChan := make(chan error)

	mux := http.NewServeMux()
	server := &http.Server{
		Addr:         ":8085",
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// Extract parameters

		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errorParam := r.URL.Query().Get("error")
		errorDesc := r.URL.Query().Get("error_description")

		// check for errors

		if errorParam != "" {
			logger.Error("Authorisation error: %v", errorParam)
			if errorDesc != "" {
				logger.Error("- %s ", errorDesc)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `
                <html>
                <head><title>Authentication failed</title></head>
                <body style="font-family: sans-serif; text-align: center; padding: 50px;">
                    <h1 style="color: #d32f2f;">Authentication failed</h1>
                    <p>%s</p>
                    <p>You can close this window.</p>
                </body>
                </html>
            `, errorParam)

			errChan <- logger.Errorf(errorParam + errorDesc)
			return
		}

		// no errors then validate the required parameters

		if code == "" {
			http.Error(w, "missing auth code", http.StatusBadRequest)
			errorMessage := "missing authorisation code in callback"
			logger.Error(errorMessage)
			errChan <- errors.New(errorMessage)
		}

		if state == "" {
			http.Error(w, "Missing state parameter", http.StatusBadRequest)
			errorMessage := "missing state param in callback"
			logger.Error(errorMessage)
			errChan <- errors.New(errorMessage)
		}

		// verify state
		session.mu.Lock()
		expectedState := session.state
		session.mu.Unlock()
		logger.Info("reviewing state")
		if state != expectedState {
			http.Error(w, "Invalid state parameter", http.StatusBadRequest)
			errorMessage := "state mismatch - possible CSRF attack"
			logger.Error(errorMessage)
			errChan <- errors.New(errorMessage)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `
            <html>
            <head><title>Authentication successful</title></head>
            <body style="font-family: sans-serif; text-align: center; padding: 50px;">
            <h1 style="color: #4caf50;">Authentication successful</h1>
            <p>You can close this window and return to your application.</p>
            </body>
            </html>
            `)

		// send code to the main go routine

		select {
		case codeChan <- code:
		default:
		}
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})

	go func() {
		logger.Info("Starting local callback server on localhost:8085")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("callback server error: %w", err)
			errChan <- errors.New(err.Error())
		}
	}()

	select {
	case code := <-codeChan:
		// Graceful shutdown with timeout
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
		return code, nil

	case err := <-errChan:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
		return "", err

	case <-time.After(5 * time.Minute):
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
		return "", logger.Errorf("timeout waiting for authentication callback")

	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
		return "", logger.Errorf("authentication cancelled: %w", ctx.Err())
	}

}

func exchangeCodeForTokens(code, code_verifer string) (*TokenResponse, error) {
	tenant_url := os.Getenv("AUTH0_TENANT")

	client_id := os.Getenv("AUTH0_NATIVE_CLIENT_ID")
	if tenant_url == "" || client_id == "" {
		logger.Fatal("auth0 config missing for authentication")
	}
	callbackurl := os.Getenv("CALLBACK_URL")
	if callbackurl == "" {
		logger.Error("callback url missing for authentication")
	}
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", client_id)
	data.Set("code", code)
	data.Set("redirect_uri", callbackurl)
	data.Set("code_verifier", code_verifer)

	token_url := "https://" + tenant_url + "/oauth/token"

	req, err := http.NewRequest("POST", token_url, strings.NewReader(data.Encode()))
	if err != nil {
		logger.Error("failed to create token request, %v", err)
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	response, err := client.Do(req)
	if err != nil {
		logger.Error("failed to exchange code for tokens: %v", err)
		return nil, err
	}

	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		logger.Error("failed to read token response: %v", err)
	}

	if response.StatusCode != http.StatusOK {

		logger.Error("token exchange failed with status %d: %s", response.StatusCode, err)
		return nil, errors.New(strconv.Itoa(response.StatusCode) + string(body))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {

		return nil, logger.Errorf("failed to parse token response: %w", err)
	}

	return &tokens, nil
}

func login(cmd *cobra.Command, args []string) {

	// checking if already logged in
	if loginStatus() {
		logger.Printf("Skipping authentication, already logged in")
		return
	}

	logger.Printf("starting pkce login flow")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Generating code_verifier
	code_verifier, err := generateCodeVerifier(32)
	if err != nil {
		logger.Fatal("unable to create code verifier: %v", err)
	}
	// Generating code_challenge
	code_challenge := generateCodeChallenge(code_verifier)

	// Generate state
	state, err := generateCodeVerifier(24)
	if err != nil {
		log.Fatal("unable to generate state: ", err)
	}
	session := &OAuthSession{
		state:        state,
		codeVerifier: code_verifier,
	}
	// build authorisation url
	auth_url := buildAuthURL(code_verifier, code_challenge, state)
	logger.Info("url: %v", auth_url)

	// launch a browser with the auth url in user's default browser
	codeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	go func() {
		code, err := startCallbackServer(ctx, session)
		if err != nil {
			logger.Error("Error fetching auth code: %v", err)
			errChan <- err
			return
		}
		codeChan <- code
	}()
	time.Sleep(100 * time.Millisecond)
	err = openBrowser(auth_url)
	if err != nil {
		logger.Error("unable to open browser for authentication: %v", err)
		logger.Info("Please manually open this URL in your browser:\n%s\n\n", auth_url)
	}

	// wait for authorisation code
	code := <-codeChan

	logger.Info("authorisation code : %v", code)

	// Exchange auth code for tokens from auth-

	logger.Info("exhanging authorisation code for tokens")
	tokens, err := exchangeCodeForTokens(code, code_verifier)
	if err != nil {
		logger.Fatal("failed to exchange code for tokens: %v", err)
	}
	// parsing the token for user info - email, name

	userInfo, err := parseIDToken(tokens.IDToken)
	if err != nil {
		logger.Error("failed to parse id token: %v", err)
		userInfo = &UserInfo{Email: "unkown", Name: "unknown"}
	}
	// fetching the role from access token
	accessToken, err := parseAccessToken(tokens.AccessToken)
	if err != nil {
		logger.Error("unabled to parse access token: %v", err)
	} else {
		userInfo.Roles = extractRolesFromClaims(accessToken)
	}

	// save in creds obj

	creds := SavedCreds{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second),
		IssuedAt:     time.Now(),
		UserInfo:     *userInfo,
	}

	if err := saveCreds(&creds); err != nil {
		logger.Fatal("failed to saved creds: %v", err)
	}

	logger.Info("Authentication successful")
	logger.Info("logged in as: %s (%s), role: %v", userInfo.Name, userInfo.Email, userInfo.Roles)
	logger.Info("token expires in : %s", creds.ExpiresAt.Format(time.RFC1123))
	logger.Info("\nCredentials saved to: ~/.gprxy/credentials")
}
