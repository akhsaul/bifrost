package antigravity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// DefaultAntigravityClientID is the public Google OAuth client ID used by the Antigravity CLI / Cloud Code.
	DefaultAntigravityClientID = ""
	// DefaultAntigravityClientSecret is the public Google OAuth client secret for Antigravity.
	DefaultAntigravityClientSecret = "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"

	GoogleOAuthTokenURL   = "https://oauth2.googleapis.com/token"
	DefaultRuntimeBaseURL = "https://cloudcode-pa.googleapis.com"
	LoadCodeAssistPath    = "/v1internal:loadCodeAssist"
	StreamGeneratePath    = "/v1internal:streamGenerateContent?alt=sse"
	FetchModelsPath       = "/v1internal:fetchAvailableModels"

	DefaultClientProfile = "ide"
	DefaultIDEVersion    = "0.1.0"
	DefaultOS            = "darwin"
	DefaultArch          = "arm64"
)

// CachedToken holds access token, expiration time, and discovered project ID for a credential set.
type CachedToken struct {
	AccessToken string
	ExpiresAt   time.Time
	ProjectID   string
}

var (
	tokenCacheMu sync.RWMutex
	tokenCache   = make(map[string]*CachedToken)
)

// ClearTokenCache clears the in-memory token cache (useful for tests).
func ClearTokenCache() {
	tokenCacheMu.Lock()
	defer tokenCacheMu.Unlock()
	tokenCache = make(map[string]*CachedToken)
}

// GetUserAgent returns the appropriate User-Agent string for the given Antigravity client profile.
func GetUserAgent(profile string) string {
	if strings.ToLower(profile) == "cli" {
		return fmt.Sprintf("antigravity/cli/%s (aidev_client; os_type=%s; arch=%s; auth_method=consumer)", DefaultIDEVersion, DefaultOS, DefaultArch)
	}
	return fmt.Sprintf("antigravity/ide/%s %s/%s", DefaultIDEVersion, DefaultOS, DefaultArch)
}

// GenerateAntigravityRequestID generates a unique request ID with the format agent/<timestamp>/<hex>.
func GenerateAntigravityRequestID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("agent/%d/%08x", time.Now().UnixMilli(), time.Now().UnixNano()%0xffffffff)
	}
	return fmt.Sprintf("agent/%d/%s", time.Now().UnixMilli(), hex.EncodeToString(b))
}

// GetCredentials extracts and normalizes AntigravityCredentials from a schemas.Key.
func GetCredentials(key schemas.Key) *AntigravityCredentials {
	creds := &AntigravityCredentials{
		ClientProfile: DefaultClientProfile,
	}

	if key.AntigravityKeyConfig != nil {
		cfg := key.AntigravityKeyConfig
		if cfg.ProjectID != nil {
			creds.ProjectID = strings.TrimSpace(cfg.ProjectID.GetValue())
		}
		if cfg.RefreshToken != nil {
			creds.RefreshToken = strings.TrimSpace(cfg.RefreshToken.GetValue())
		}
		if cfg.AccessToken != nil {
			creds.AccessToken = strings.TrimSpace(cfg.AccessToken.GetValue())
		}
		if cfg.ClientID != nil {
			creds.ClientID = strings.TrimSpace(cfg.ClientID.GetValue())
		}
		if cfg.ClientSecret != nil {
			creds.ClientSecret = strings.TrimSpace(cfg.ClientSecret.GetValue())
		}
		if cfg.ClientProfile != nil && *cfg.ClientProfile != "" {
			creds.ClientProfile = strings.TrimSpace(*cfg.ClientProfile)
		}
	}

	val := strings.TrimSpace(key.Value.GetValue())
	if val != "" {
		if strings.HasPrefix(val, "{") {
			var jsonCreds AntigravityCredentials
			if err := sonic.Unmarshal([]byte(val), &jsonCreds); err == nil {
				if creds.ProjectID == "" && jsonCreds.ProjectID != "" {
					creds.ProjectID = strings.TrimSpace(jsonCreds.ProjectID)
				}
				if creds.RefreshToken == "" && jsonCreds.RefreshToken != "" {
					creds.RefreshToken = strings.TrimSpace(jsonCreds.RefreshToken)
				}
				if creds.AccessToken == "" && jsonCreds.AccessToken != "" {
					creds.AccessToken = strings.TrimSpace(jsonCreds.AccessToken)
				}
				if creds.ClientID == "" && jsonCreds.ClientID != "" {
					creds.ClientID = strings.TrimSpace(jsonCreds.ClientID)
				}
				if creds.ClientSecret == "" && jsonCreds.ClientSecret != "" {
					creds.ClientSecret = strings.TrimSpace(jsonCreds.ClientSecret)
				}
				if jsonCreds.ClientProfile != "" {
					creds.ClientProfile = strings.TrimSpace(jsonCreds.ClientProfile)
				}
			}
		} else if strings.HasPrefix(val, "ya29.") {
			if creds.AccessToken == "" {
				creds.AccessToken = val
			}
		} else {
			if creds.RefreshToken == "" {
				creds.RefreshToken = val
			}
		}
	}

	if creds.ClientID == "" {
		if envID := os.Getenv("ANTIGRAVITY_OAUTH_CLIENT_ID"); envID != "" {
			creds.ClientID = envID
		} else {
			creds.ClientID = DefaultAntigravityClientID
		}
	}

	if creds.ClientSecret == "" {
		if envSec := os.Getenv("ANTIGRAVITY_OAUTH_CLIENT_SECRET"); envSec != "" {
			creds.ClientSecret = envSec
		} else {
			creds.ClientSecret = DefaultAntigravityClientSecret
		}
	}

	return creds
}

// computeCacheKey returns a deterministic hash key for token caching.
func computeCacheKey(keyID string, creds *AntigravityCredentials) string {
	raw := fmt.Sprintf("%s:%s:%s:%s", keyID, creds.RefreshToken, creds.AccessToken, creds.ClientID)
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// GetAccessTokenAndProject retrieves a valid access token and project ID for the given key.
// It handles token caching, automatic refresh on expiration, and lazy project discovery via loadCodeAssist.
func GetAccessTokenAndProject(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	key schemas.Key,
	baseURL string,
	logger schemas.Logger,
) (string, string, *schemas.BifrostError) {
	creds := GetCredentials(key)
	if creds.RefreshToken == "" && creds.AccessToken == "" {
		return "", "", newAuthenticationError("missing Antigravity credentials (no refresh_token or access_token provided)", nil)
	}

	cacheKey := computeCacheKey(key.ID, creds)

	tokenCacheMu.RLock()
	cached, found := tokenCache[cacheKey]
	tokenCacheMu.RUnlock()

	now := time.Now()
	if found && cached != nil && cached.AccessToken != "" && now.Add(60*time.Second).Before(cached.ExpiresAt) {
		projectID := creds.ProjectID
		if projectID == "" {
			projectID = cached.ProjectID
		}
		if projectID != "" {
			return cached.AccessToken, projectID, nil
		}
	}

	var (
		accessToken string
		expiresAt   time.Time
		projectID   = creds.ProjectID
	)

	if creds.RefreshToken != "" {
		newTok, exp, err := RefreshToken(ctx, client, creds, logger)
		if err != nil {
			// If refresh fails but we have an unexpired cached/direct access token, try to fall back
			if creds.AccessToken != "" {
				accessToken = creds.AccessToken
				expiresAt = now.Add(30 * time.Minute)
			} else {
				return "", "", newAuthenticationError(fmt.Sprintf("failed to refresh Antigravity OAuth token: %v", err), err)
			}
		} else {
			accessToken = newTok
			expiresAt = exp
		}
	} else {
		accessToken = creds.AccessToken
		expiresAt = now.Add(30 * time.Minute)
	}

	if projectID == "" && cached != nil && cached.ProjectID != "" {
		projectID = cached.ProjectID
	}

	if projectID == "" {
		discovered, err := EnsureProjectID(ctx, client, accessToken, baseURL, creds.ClientProfile, logger)
		if err == nil && discovered != "" {
			projectID = discovered
		} else {
			if logger != nil {
				logger.Warn("Antigravity loadCodeAssist project discovery failed: %v", err)
			}
		}
	}

	tokenCacheMu.Lock()
	tokenCache[cacheKey] = &CachedToken{
		AccessToken: accessToken,
		ExpiresAt:   expiresAt,
		ProjectID:   projectID,
	}
	tokenCacheMu.Unlock()

	return accessToken, projectID, nil
}

// RefreshToken executes Google OAuth2 token refresh flow using the refresh_token.
func RefreshToken(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	creds *AntigravityCredentials,
	logger schemas.Logger,
) (string, time.Time, error) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", creds.RefreshToken)
	if creds.ClientID != "" {
		form.Set("client_id", creds.ClientID)
	}
	if creds.ClientSecret != "" {
		form.Set("client_secret", creds.ClientSecret)
	}

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(GoogleOAuthTokenURL)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", GetUserAgent(creds.ClientProfile))
	req.SetBodyString(form.Encode())

	var err error
	if client != nil {
		err = client.Do(req, resp)
	} else {
		err = fasthttp.Do(req, resp)
	}

	if err != nil {
		return "", time.Time{}, fmt.Errorf("network error during token refresh: %w", err)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return "", time.Time{}, fmt.Errorf("token refresh returned status %d: %s", resp.StatusCode(), string(resp.Body()))
	}

	var tokResp AntigravityTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &tokResp); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to decode token refresh response: %w", err)
	}

	if tokResp.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("token refresh response contained empty access_token: %s", string(resp.Body()))
	}

	expiresIn := tokResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if logger != nil {
		logger.Debug("Antigravity OAuth token refreshed successfully (expires in %ds)", expiresIn)
	}

	return tokResp.AccessToken, expiresAt, nil
}

// EnsureProjectID calls loadCodeAssist to discover the user's Cloud Code project ID.
func EnsureProjectID(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	accessToken string,
	baseURL string,
	profile string,
	logger schemas.Logger,
) (string, error) {
	if baseURL == "" {
		baseURL = DefaultRuntimeBaseURL
	}
	targetURL := strings.TrimRight(baseURL, "/") + LoadCodeAssistPath

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	bodyBytes, _ := sonic.Marshal(map[string]interface{}{
		"metadata": map[string]string{
			"ideType": "ANTIGRAVITY",
		},
	})

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(targetURL)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", GetUserAgent(profile))
	req.SetBody(bodyBytes)

	var err error
	if client != nil {
		err = client.Do(req, resp)
	} else {
		err = fasthttp.Do(req, resp)
	}

	if err != nil {
		return "", fmt.Errorf("loadCodeAssist network error: %w", err)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return "", fmt.Errorf("loadCodeAssist returned status %d: %s", resp.StatusCode(), string(resp.Body()))
	}

	var assistResp AntigravityLoadCodeAssistResponse
	if err := sonic.Unmarshal(resp.Body(), &assistResp); err != nil {
		return "", fmt.Errorf("failed to decode loadCodeAssist response: %w", err)
	}

	switch v := assistResp.CloudAICompanionProject.(type) {
	case string:
		return strings.TrimSpace(v), nil
	case map[string]interface{}:
		if id, ok := v["id"].(string); ok {
			return strings.TrimSpace(id), nil
		}
	}

	return "", fmt.Errorf("no project ID found in loadCodeAssist response: %s", string(resp.Body()))
}
