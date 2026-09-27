package cline

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// tokenRefreshMargin is how long before expiry an access token is
	// considered stale. Cline access tokens live about an hour.
	tokenRefreshMargin = 60 * time.Second
	// maxTokenLifetime rejects absurd expiry values rather than trusting them.
	maxTokenLifetime = 24 * time.Hour
	// maxRefreshBodyBytes caps the auth refresh response. It carries tokens,
	// never a large payload.
	maxRefreshBodyBytes = 64 * 1024
	// refreshTimeout bounds the whole token refresh call.
	refreshTimeout = 20 * time.Second

	// Backoff for a cached refresh failure. Permanent faults are configuration
	// errors and there is no point retrying them quickly; transient ones
	// deserve a short pause.
	permanentBackoffBase = 2 * time.Second
	permanentBackoffCap  = 60 * time.Second
	transientBackoffBase = 500 * time.Millisecond
	transientBackoffCap  = 10 * time.Second

	cacheKeyVersion = "cline-v1"
)

// clineToken is one cached OAuth access token. Immutable once stored.
type clineToken struct {
	accessToken string
	expiresAt   time.Time
}

// tokenFailure is the negative cache. Without it, a misconfigured key issues
// a refresh call for every inbound request.
type tokenFailure struct {
	err        *schemas.BifrostError
	retryAfter time.Time
	attempts   int
}

// clineTokenEntry is one cache slot behind a single refresh lock. The live
// refresh token rides along: Cline may rotate it on refresh, and the rotated
// value — not the configured one — must mint the next access token.
type clineTokenEntry struct {
	mu           sync.Mutex
	token        *clineToken
	refreshToken string
	failure      *tokenFailure
}

// clineTokenPool maps cache key to *clineTokenEntry. Entries are never
// deleted: the entry is the lock and holds the negative cache, and eviction
// would reset failure backoff on every rotation. Stale entries cost a few
// hundred bytes, bounded by the number of distinct historical credentials.
var clineTokenPool sync.Map

// resolveCredentials produces the Authorization header value for one request.
//
// Two auth modes, checked in this order:
//
//  1. A static Cline API key in Key.Value, sent verbatim as "Bearer <value>".
//     This is what operators paste from the Cline dashboard.
//  2. ClineKeyConfig holding a WorkOS-device-flow refresh token. Bifrost
//     exchanges it at /v1/auth/refresh for a short-lived access token, sent as
//     "Bearer workos:<access_token>" — the workos: prefix is required, without
//     it Cline rejects the call.
//
// The returned bool reports OAuth mode (true) so callers know a 401 is worth
// one invalidate-and-retry.
func resolveCredentials(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	client *fasthttp.Client,
	refreshURL string,
) (string, bool, *schemas.BifrostError) {
	if token := strings.TrimSpace(key.Value.GetValue()); token != "" {
		return "Bearer " + token, false, nil
	}

	if key.ClineKeyConfig == nil || strings.TrimSpace(key.ClineKeyConfig.RefreshToken.GetValue()) == "" {
		return "", false, configurationError(
			"cline: no credentials on this key. Set either value (a Cline API key) " +
				"or cline_key_config.refresh_token (OAuth refresh token from the WorkOS device flow).",
		)
	}

	token, bErr := getAccessToken(ctx, key, client, refreshURL)
	if bErr != nil {
		return "", false, bErr
	}
	return "Bearer " + OAuthTokenPrefix + token, true, nil
}

// clineCacheKey derives the pool key from every input that changes the
// identity of the minted token. Fields are length-framed so two credential
// tuples can never collide on a separator.
func clineCacheKey(key schemas.Key) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	field(cacheKeyVersion)
	field(key.ID)
	field(key.ClineKeyConfig.ClientID.GetValue())
	field(key.ClineKeyConfig.RefreshToken.GetValue())
	return hex.EncodeToString(h.Sum(nil))
}

// getAccessToken returns a valid OAuth access token for the key, refreshing
// and caching as needed.
func getAccessToken(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	client *fasthttp.Client,
	refreshURL string,
) (string, *schemas.BifrostError) {
	cacheKey := clineCacheKey(key)
	entry := loadOrCreateEntry(cacheKey)
	now := time.Now()

	if tok := entry.token; tok != nil && now.Before(tok.expiresAt.Add(-tokenRefreshMargin)) {
		return tok.accessToken, nil
	}
	if f := entry.failure; f != nil && now.Before(f.retryAfter) {
		return "", f.err
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	now = time.Now()
	if tok := entry.token; tok != nil && now.Before(tok.expiresAt.Add(-tokenRefreshMargin)) {
		return tok.accessToken, nil
	}
	if f := entry.failure; f != nil && now.Before(f.retryAfter) {
		return "", f.err
	}

	// WithoutCancel: the caller that happens to trigger a refresh may hang up,
	// but the refresh result serves every waiter. Killing it on one client's
	// disconnect would turn one cancellation into many failures.
	parent := context.Background()
	if ctx != nil {
		parent = context.WithoutCancel(ctx)
	}
	refreshCtx, cancel := context.WithTimeout(parent, refreshTimeout)
	defer cancel()

	tok, rotatedRefreshToken, bErr := exchangeRefreshToken(refreshCtx, client, entry.liveRefreshToken(key), refreshURL)
	if bErr != nil {
		entry.failure = &tokenFailure{
			err:        bErr,
			retryAfter: time.Now().Add(backoffFor(failureAttempts(entry), isPermanentError(bErr))),
			attempts:   failureAttempts(entry) + 1,
		}
		return "", bErr
	}
	entry.token = tok
	entry.adoptRefreshToken(rotatedRefreshToken)
	entry.failure = nil
	return tok.accessToken, nil
}

// liveRefreshToken returns the refresh token to mint with: the adopted
// rotated value when one exists, otherwise the configured one.
func (e *clineTokenEntry) liveRefreshToken(key schemas.Key) string {
	if e.refreshToken != "" {
		return e.refreshToken
	}
	return strings.TrimSpace(key.ClineKeyConfig.RefreshToken.GetValue())
}

// adoptRefreshToken records a rotated refresh token from a successful refresh.
// Empty means the response carried none; the configured value stays live.
func (e *clineTokenEntry) adoptRefreshToken(rotated string) {
	if strings.TrimSpace(rotated) != "" {
		e.refreshToken = rotated
	}
}

func loadOrCreateEntry(cacheKey string) *clineTokenEntry {
	if existing, ok := clineTokenPool.Load(cacheKey); ok {
		return existing.(*clineTokenEntry)
	}
	actual, _ := clineTokenPool.LoadOrStore(cacheKey, &clineTokenEntry{})
	return actual.(*clineTokenEntry)
}

func failureAttempts(entry *clineTokenEntry) int {
	if entry.failure != nil {
		return entry.failure.attempts
	}
	return 0
}

// backoffFor grows the retry delay geometrically and clamps it.
func backoffFor(attempts int, permanent bool) time.Duration {
	base, cap := transientBackoffBase, transientBackoffCap
	if permanent {
		base, cap = permanentBackoffBase, permanentBackoffCap
	}
	delay := base
	for i := 1; i < attempts && delay < cap; i++ {
		delay *= 2
	}
	if delay > cap {
		delay = cap
	}
	return delay
}

// isPermanentError reports whether a refresh failure is a configuration fault
// rather than a transient one. Rate limits and server errors are transient;
// everything else on the credential path means something is misconfigured.
func isPermanentError(bErr *schemas.BifrostError) bool {
	if bErr == nil || bErr.StatusCode == nil {
		return false
	}
	switch status := *bErr.StatusCode; {
	case status == http.StatusTooManyRequests, status >= 500:
		return false
	default:
		return true
	}
}

// invalidateCredentials discards the cached access token for a key after an
// inference call rejected it with 401. The refresh token itself is untouched,
// so the next request mints a fresh access token from it.
func invalidateCredentials(key *schemas.Key) {
	if key == nil || key.ClineKeyConfig == nil {
		return
	}
	if entry, ok := clineTokenPool.Load(clineCacheKey(*key)); ok {
		e := entry.(*clineTokenEntry)
		e.mu.Lock()
		e.token = nil
		e.mu.Unlock()
	}
}

// exchangeRefreshToken performs the OAuth refresh: refresh token in, access
// token and expiry out.
func exchangeRefreshToken(
	ctx context.Context,
	client *fasthttp.Client,
	refreshToken string,
	refreshURL string,
) (*clineToken, string, *schemas.BifrostError) {
	body, err := sonic.Marshal(ClineRefreshRequest{
		RefreshToken: refreshToken,
		GrantType:    "refresh_token",
	})
	if err != nil {
		return nil, "", configurationError("cline: could not build the token refresh request: " + err.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(refreshURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "node")
	req.SetBody(body)

	if deadline, ok := ctx.Deadline(); ok {
		if err := client.DoDeadline(req, resp, deadline); err != nil {
			return nil, "", providerUtils.NewProviderAPIError("cline: could not reach Cline to refresh the OAuth token", err, 0, nil, nil)
		}
	} else {
		if err := client.DoTimeout(req, resp, refreshTimeout); err != nil {
			return nil, "", providerUtils.NewProviderAPIError("cline: could not reach Cline to refresh the OAuth token", err, 0, nil, nil)
		}
	}

	if status := resp.StatusCode(); status != http.StatusOK {
		return nil, "", classifyRefreshError(status, resp.Body())
	}

	if len(resp.Body()) > maxRefreshBodyBytes {
		return nil, "", providerUtils.NewProviderAPIError("cline: token refresh response exceeded size limit", nil, resp.StatusCode(), nil, nil)
	}

	var parsed ClineRefreshResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, "", providerUtils.NewProviderAPIError("cline: could not parse Cline's token refresh response", err, resp.StatusCode(), nil, nil)
	}
	if !parsed.Success || parsed.Data.AccessToken == "" {
		return nil, "", blockingError("cline: Cline refused the refresh token", resp.StatusCode())
	}

	expiresAt, err := time.Parse(time.RFC3339, parsed.Data.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now()) {
		return nil, "", blockingError("cline: Cline returned a missing or expired access token expiry", resp.StatusCode())
	}
	if expiresAt.Sub(time.Now()) > maxTokenLifetime {
		return nil, "", blockingError("cline: Cline returned an implausible access token expiry", resp.StatusCode())
	}

	return &clineToken{
		accessToken: parsed.Data.AccessToken,
		expiresAt:   expiresAt,
	}, parsed.Data.RefreshToken, nil
}

// classifyRefreshError maps a refresh failure. A failure here is about the
// stored refresh token, never about the request that triggered it.
func classifyRefreshError(status int, body []byte) *schemas.BifrostError {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return blockingError(fmt.Sprintf("cline: Cline rejected the OAuth refresh token (%d). Re-run the WorkOS device flow and update cline_key_config.refresh_token.", status), status)
	default:
		return providerUtils.NewProviderAPIError(
			fmt.Sprintf("cline: token refresh failed (%d)", status), nil, status, nil, nil)
	}
}

// configurationError builds a setup-fault error that must not drain onto a
// fallback provider.
func configurationError(message string) *schemas.BifrostError {
	bErr := providerUtils.NewProviderAPIError(message, nil, 0, nil, nil)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}

// blockingError builds a credential-fault error that must not drain onto a
// fallback provider.
func blockingError(message string, statusCode int) *schemas.BifrostError {
	bErr := providerUtils.NewProviderAPIError(message, nil, statusCode, nil, nil)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}

// isUnauthorized reports whether the error is an upstream 401.
func isUnauthorized(bErr *schemas.BifrostError) bool {
	return bErr != nil && bErr.StatusCode != nil && *bErr.StatusCode == http.StatusUnauthorized
}
