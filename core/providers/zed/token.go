package zed

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
	// tokenRefreshMargin is how long before the organization lookup is
	// considered stale. LLM tokens are minted per (user, org) and cached;
	// the users/me org lookup is cached alongside them.
	tokenRefreshMargin = 60 * time.Second
	// maxTokenLifetime rejects absurd cache lifetimes rather than trusting them.
	maxTokenLifetime = 24 * time.Hour
	// refreshTimeout bounds a single users/me or llm_tokens call.
	refreshTimeout = 20 * time.Second
	// maxAuthBodyBytes caps the users/me and llm_tokens responses.
	maxAuthBodyBytes = 256 * 1024

	cacheKeyVersion = "zed-v1"
)

// zedCredentials is one cached credential set: the short-lived LLM bearer
// token plus the resolved organization it was minted for. Immutable once stored.
type zedCredentials struct {
	llmToken       string
	organizationID string
	fetchedAt      time.Time
}

// tokenFailure is the negative cache. Without it, a misconfigured key issues
// auth calls for every inbound request.
type tokenFailure struct {
	err        *schemas.BifrostError
	retryAfter time.Time
	attempts   int
}

// zedTokenEntry is one cache slot behind a single refresh lock.
type zedTokenEntry struct {
	mu      sync.Mutex
	creds   *zedCredentials
	failure *tokenFailure
}

// zedTokenPool maps cache key to *zedTokenEntry. Entries are never deleted:
// the entry is the lock and holds the negative cache, and eviction would
// reset failure backoff. Stale entries cost a few hundred bytes, bounded by
// the number of distinct historical credentials.
var zedTokenPool sync.Map

// resolveSystemID returns the x-zed-system-id for a key: the configured
// value, or the well-known editor default when empty.
func resolveSystemID(key *schemas.Key) string {
	if key != nil && key.ZedKeyConfig != nil && key.ZedKeyConfig.SystemID != nil {
		if id := strings.TrimSpace(key.ZedKeyConfig.SystemID.GetValue()); id != "" {
			return id
		}
	}
	return DefaultSystemID
}

// userAuthValue builds the "authorization" header value for the user-identity
// endpoints (/client/users/me, /client/llm_tokens):
// "<user_id> <access_token-blob-verbatim>".
func userAuthValue(key schemas.Key) (string, *schemas.BifrostError) {
	cfg := key.ZedKeyConfig
	if cfg == nil {
		return "", configurationError("zed: zed_key_config is required (user_id + access_token)")
	}
	userID := strings.TrimSpace(cfg.UserID.GetValue())
	blob := strings.TrimSpace(cfg.AccessToken.GetValue())
	if userID == "" || blob == "" {
		return "", configurationError("zed: zed_key_config.user_id and zed_key_config.access_token are both required")
	}
	return userID + " " + blob, nil
}

// zedCacheKey derives the pool key from every input that changes the identity
// of the minted LLM token. Fields are length-framed so two credential tuples
// can never collide on a separator.
func zedCacheKey(key schemas.Key) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	field(cacheKeyVersion)
	field(key.ID)
	cfg := key.ZedKeyConfig
	if cfg != nil {
		field(cfg.UserID.GetValue())
		field(cfg.AccessToken.GetValue())
		if cfg.OrganizationID != nil {
			field(cfg.OrganizationID.GetValue())
		}
		if cfg.SystemID != nil {
			field(cfg.SystemID.GetValue())
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resolveCredentials produces the Authorization header value for one
// /completions or /models request: "Bearer <llm_token>", minting and caching
// the token as needed. The returned bool reports OAuth mode (always true for
// zed) so callers know a 401 is worth one invalidate-and-retry.
func (provider *ZedProvider) resolveCredentials(ctx *schemas.BifrostContext, key schemas.Key) (string, bool, *schemas.BifrostError) {
	if key.ZedKeyConfig == nil {
		return "", false, configurationError("zed: zed_key_config is required (user_id + access_token)")
	}
	token, bErr := provider.getLLMToken(ctx, key)
	if bErr != nil {
		return "", false, bErr
	}
	return "Bearer " + token, true, nil
}

// getLLMToken returns a valid LLM bearer token for the key, minting and
// caching as needed.
func (provider *ZedProvider) getLLMToken(ctx *schemas.BifrostContext, key schemas.Key) (string, *schemas.BifrostError) {
	cacheKey := zedCacheKey(key)
	entry := loadOrCreateTokenEntry(cacheKey)
	now := time.Now()

	if creds := entry.creds; creds != nil && now.Before(creds.fetchedAt.Add(maxTokenLifetime).Add(-tokenRefreshMargin)) {
		return creds.llmToken, nil
	}
	if f := entry.failure; f != nil && now.Before(f.retryAfter) {
		return "", f.err
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	now = time.Now()
	if creds := entry.creds; creds != nil && now.Before(creds.fetchedAt.Add(maxTokenLifetime).Add(-tokenRefreshMargin)) {
		return creds.llmToken, nil
	}
	if f := entry.failure; f != nil && now.Before(f.retryAfter) {
		return "", f.err
	}

	// WithoutCancel: the caller that happens to trigger a mint may hang up,
	// but the minted token serves every waiter. Killing it on one client's
	// disconnect would turn one cancellation into many failures.
	parent := context.Background()
	if ctx != nil {
		parent = context.WithoutCancel(ctx)
	}
	mintCtx, cancel := context.WithTimeout(parent, 2*refreshTimeout)
	defer cancel()

	creds, bErr := provider.mintLLMToken(mintCtx, key)
	if bErr != nil {
		entry.failure = &tokenFailure{
			err:        bErr,
			retryAfter: time.Now().Add(backoffFor(failureAttempts(entry), isPermanentError(bErr))),
			attempts:   failureAttempts(entry) + 1,
		}
		return "", bErr
	}
	entry.creds = creds
	entry.failure = nil
	return creds.llmToken, nil
}

func loadOrCreateTokenEntry(cacheKey string) *zedTokenEntry {
	if existing, ok := zedTokenPool.Load(cacheKey); ok {
		return existing.(*zedTokenEntry)
	}
	actual, _ := zedTokenPool.LoadOrStore(cacheKey, &zedTokenEntry{})
	return actual.(*zedTokenEntry)
}

func failureAttempts(entry *zedTokenEntry) int {
	if entry.failure != nil {
		return entry.failure.attempts
	}
	return 0
}

// backoffFor grows the retry delay geometrically and clamps it.
func backoffFor(attempts int, permanent bool) time.Duration {
	base, cap := 500*time.Millisecond, 10*time.Second
	if permanent {
		base, cap = 2*time.Second, 60*time.Second
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

// isPermanentError reports whether a mint failure is a configuration fault
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

// invalidateCredentials discards the cached LLM token for a key after an
// inference call rejected it with 401. The login material is untouched, so
// the next request mints a fresh LLM token from it.
func invalidateCredentials(key *schemas.Key) {
	if key == nil || key.ZedKeyConfig == nil {
		return
	}
	if entry, ok := zedTokenPool.Load(zedCacheKey(*key)); ok {
		e := entry.(*zedTokenEntry)
		e.mu.Lock()
		e.creds = nil
		e.mu.Unlock()
	}
}

// mintLLMToken performs the login chain: resolve the organization (explicit
// config or default_organization_id from /client/users/me), then mint the
// short-lived LLM token via POST /client/llm_tokens.
func (provider *ZedProvider) mintLLMToken(ctx context.Context, key schemas.Key) (*zedCredentials, *schemas.BifrostError) {
	userAuth, bErr := userAuthValue(key)
	if bErr != nil {
		return nil, bErr
	}
	orgID := ""
	if key.ZedKeyConfig.OrganizationID != nil {
		orgID = strings.TrimSpace(key.ZedKeyConfig.OrganizationID.GetValue())
	}
	if orgID == "" {
		var fetchErr *schemas.BifrostError
		orgID, fetchErr = provider.fetchDefaultOrganization(ctx, key, userAuth)
		if fetchErr != nil {
			return nil, fetchErr
		}
	}
	token, bErr := provider.fetchLLMToken(ctx, key, userAuth, orgID)
	if bErr != nil {
		return nil, bErr
	}
	return &zedCredentials{llmToken: token, organizationID: orgID, fetchedAt: time.Now()}, nil
}

// fetchDefaultOrganization resolves default_organization_id via
// GET /client/users/me.
func (provider *ZedProvider) fetchDefaultOrganization(ctx context.Context, key schemas.Key, userAuth string) (string, *schemas.BifrostError) {
	body, status, bErr := provider.doUserAuthGet(ctx, key, provider.networkConfig.BaseURL+provider.usersMePath, userAuth)
	if bErr != nil {
		return "", bErr
	}
	if status != http.StatusOK {
		return "", classifyAuthError("zed: fetching the default organization failed", status)
	}
	var parsed ZedUsersMeResponse
	if err := sonic.Unmarshal(body, &parsed); err != nil {
		return "", providerUtils.NewProviderAPIError("zed: could not parse the users/me response", err, status, nil, nil)
	}
	if strings.TrimSpace(parsed.DefaultOrganizationID) == "" {
		return "", blockingError("zed: Zed returned no default_organization_id; set zed_key_config.organization_id explicitly", status)
	}
	return strings.TrimSpace(parsed.DefaultOrganizationID), nil
}

// fetchLLMToken mints the short-lived inference token via
// POST /client/llm_tokens.
func (provider *ZedProvider) fetchLLMToken(ctx context.Context, key schemas.Key, userAuth, orgID string) (string, *schemas.BifrostError) {
	reqBody, err := sonic.Marshal(ZedLLMTokensRequest{OrganizationID: orgID})
	if err != nil {
		return "", configurationError("zed: could not build the llm_tokens request: " + err.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(provider.networkConfig.BaseURL + provider.llmTokensPath)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	applyUserAuthHeaders(req, userAuth, resolveSystemID(&key), nil)
	req.SetBody(reqBody)

	if err := doWithDeadline(ctx, provider.client, req, resp); err != nil {
		return "", providerUtils.NewProviderAPIError("zed: could not reach Zed to mint the LLM token", err, 0, nil, nil)
	}
	if resp.StatusCode() != http.StatusOK {
		return "", classifyAuthError("zed: minting the LLM token failed", resp.StatusCode())
	}
	if len(resp.Body()) > maxAuthBodyBytes {
		return "", providerUtils.NewProviderAPIError("zed: llm_tokens response exceeded size limit", nil, resp.StatusCode(), nil, nil)
	}
	var parsed ZedLLMTokensResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return "", providerUtils.NewProviderAPIError("zed: could not parse Zed's llm_tokens response", err, resp.StatusCode(), nil, nil)
	}
	if strings.TrimSpace(parsed.Token) == "" {
		return "", blockingError("zed: Zed returned an empty LLM token", resp.StatusCode())
	}
	return strings.TrimSpace(parsed.Token), nil
}

// doUserAuthGet performs an authenticated GET against a user-identity
// endpoint and returns a copy of the body.
func (provider *ZedProvider) doUserAuthGet(ctx context.Context, key schemas.Key, url, userAuth string) ([]byte, int, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Accept", "application/json")
	applyUserAuthHeaders(req, userAuth, resolveSystemID(&key), nil)

	if err := doWithDeadline(ctx, provider.client, req, resp); err != nil {
		return nil, 0, providerUtils.NewProviderAPIError("zed: could not reach Zed", err, 0, nil, nil)
	}
	if len(resp.Body()) > maxAuthBodyBytes {
		return nil, resp.StatusCode(), providerUtils.NewProviderAPIError("zed: response exceeded size limit", nil, resp.StatusCode(), nil, nil)
	}
	return append([]byte(nil), resp.Body()...), resp.StatusCode(), nil
}

// applyUserAuthHeaders sets the identity headers for the user-auth endpoints:
// the "<user_id> <blob>" authorization, x-zed-system-id, and user-agent.
// extra (config/context overrides) wins over defaults.
func applyUserAuthHeaders(req *fasthttp.Request, userAuth, systemID string, extra map[string]string) {
	req.Header.Set("authorization", userAuth)
	req.Header.Set("x-zed-system-id", systemID)
	req.Header.SetUserAgent(DefaultUserAgent)
	for k, v := range extra {
		if strings.EqualFold(k, "authorization") {
			continue
		}
		req.Header.Set(k, v)
	}
}

// doWithDeadline issues one request, honouring the context deadline.
func doWithDeadline(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) error {
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			return client.DoDeadline(req, resp, deadline)
		}
	}
	return client.DoTimeout(req, resp, refreshTimeout)
}

// classifyAuthError maps a user-auth failure. A failure here is about the
// stored login material, never about the request that triggered it.
func classifyAuthError(prefix string, status int) *schemas.BifrostError {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return blockingError(fmt.Sprintf("%s (%d). The user_id/access_token is rejected; log in to Zed again and update zed_key_config.", prefix, status), status)
	case http.StatusPaymentRequired:
		return blockingError(fmt.Sprintf("%s (%d). The Zed organization is out of quota or needs billing.", prefix, status), status)
	default:
		return providerUtils.NewProviderAPIError(fmt.Sprintf("%s (%d)", prefix, status), nil, status, nil, nil)
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
