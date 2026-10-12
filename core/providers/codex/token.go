package codex

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
	// considered stale. Codex access tokens live about a day (expires_in
	// 864000s in the captured exchange).
	tokenRefreshMargin = 60 * time.Second
	// accountTTL bounds how long a discovered (accountID, planType) pair is
	// trusted. Plans change rarely; rechecking daily keeps the plan filter
	// fresh without an extra request on the inference path.
	accountTTL = 24 * time.Hour
	// maxTokenLifetime rejects absurd expiry values rather than trusting them.
	maxTokenLifetime = 30 * 24 * time.Hour
	// maxExchangeBodyBytes caps every auth/account exchange response. They
	// carry tokens and account metadata, never a large payload.
	maxExchangeBodyBytes = 64 * 1024
	// exchangeTimeout bounds the whole refresh + discovery pipeline. Callers
	// queued on the entry mutex wait at most this long.
	exchangeTimeout = 20 * time.Second

	// Backoff for a cached refresh failure. Permanent faults are configuration
	// errors and there is no point retrying them quickly; transient ones
	// deserve a short pause.
	permanentBackoffBase = 2 * time.Second
	permanentBackoffCap  = 60 * time.Second
	transientBackoffBase = 500 * time.Millisecond
	transientBackoffCap  = 10 * time.Second

	cacheKeyVersion = "codex-v1"
)

// codexCredentials is everything a single inference call needs: the bearer
// token, the workspace to bind it to, and the plan gating ListModels.
type codexCredentials struct {
	// AccessToken is the ChatGPT session bearer token.
	AccessToken string
	// AccountID is the resolved workspace, sent as chatgpt-account-id. Empty
	// means discovery failed and the request goes out unbound.
	AccountID string
	// PlanType is the resolved account's plan (e.g. "free"), lowercase.
	PlanType string
}

// codexToken is one cached OAuth credential set. Immutable once stored,
// replaced wholesale on refresh.
type codexToken struct {
	accessToken  string
	expiresAt    time.Time
	refreshToken string
	accountID    string
	planType     string
	accountAt    time.Time
}

// tokenFailure is the negative cache. Without it, a misconfigured key issues
// a refresh call for every inbound request.
type tokenFailure struct {
	err        *schemas.BifrostError
	retryAfter time.Time
	attempts   int
}

// codexTokenEntry is one cache slot behind a single refresh lock. The live
// refresh token rides along: Codex rotates it on every refresh, and the
// rotated value — not the configured one — must mint the next access token.
type codexTokenEntry struct {
	mu      sync.Mutex
	token   *codexToken
	failure *tokenFailure
}

// codexTokenPool maps cache key to *codexTokenEntry. Entries are never
// deleted: the entry is the lock and holds the negative cache, and eviction
// would reset failure backoff on every rotation. Stale entries cost a few
// hundred bytes, bounded by the number of distinct historical credentials.
var codexTokenPool sync.Map

// resolveCredentials produces the credentials for one request.
//
// Two auth modes, checked in this order:
//
//  1. A static access token in Key.Value, sent verbatim. AccountID is still
//     honored when configured (it is an identifier, not a secret).
//  2. CodexKeyConfig holding a PKCE-flow refresh token. Bifrost exchanges it
//     for a short-lived access token and discovers the workspace from
//     wham/accounts/check when AccountID is not configured.
//
// The returned bool reports OAuth mode (true) so callers know a 401 is worth
// one invalidate-and-retry.
func resolveCredentials(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	client *fasthttp.Client,
) (*codexCredentials, bool, *schemas.BifrostError) {
	if token := strings.TrimSpace(key.Value.GetValue()); token != "" {
		var accountID string
		if key.CodexKeyConfig != nil {
			accountID = strings.TrimSpace(key.CodexKeyConfig.AccountID.GetValue())
		}
		return &codexCredentials{AccessToken: token, AccountID: accountID}, false, nil
	}

	if key.CodexKeyConfig == nil {
		return nil, false, configurationError(
			"codex: no credentials on this key. Set either value (an access token) " +
				"or codex_key_config.refresh_token (OAuth refresh token from the PKCE flow).",
		)
	}

	creds, bErr := mintCredentials(ctx, key, client)
	if bErr != nil {
		return nil, false, bErr
	}
	return creds, true, nil
}

// codexCacheKey derives the pool key from every input that changes the
// identity of the minted token. Fields are length-framed so two credential
// tuples can never collide on a separator.
func codexCacheKey(key schemas.Key) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	field(cacheKeyVersion)
	field(key.ID)
	field(key.CodexKeyConfig.ClientID.GetValue())
	field(key.CodexKeyConfig.RefreshToken.GetValue())
	field(key.CodexKeyConfig.AccessToken.GetValue())
	field(key.CodexKeyConfig.AccountID.GetValue())
	return hex.EncodeToString(h.Sum(nil))
}

// mintCredentials returns valid OAuth credentials for the key, refreshing and
// caching as needed.
func mintCredentials(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	client *fasthttp.Client,
) (*codexCredentials, *schemas.BifrostError) {
	cfg := key.CodexKeyConfig
	if strings.TrimSpace(cfg.RefreshToken.GetValue()) == "" && strings.TrimSpace(cfg.AccessToken.GetValue()) == "" {
		return nil, configurationError(
			"codex: codex_key_config.refresh_token is required when value is not set",
		)
	}

	cacheKey := codexCacheKey(key)
	entry := loadOrCreateEntry(cacheKey)
	now := time.Now()

	if tok := entry.token; tok != nil && now.Before(tok.expiresAt.Add(-tokenRefreshMargin)) &&
		accountFresh(tok, cfg, now) {
		return tok.credentials(), nil
	}
	if f := entry.failure; f != nil && now.Before(f.retryAfter) {
		return nil, f.err
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	now = time.Now()
	if tok := entry.token; tok != nil && now.Before(tok.expiresAt.Add(-tokenRefreshMargin)) &&
		accountFresh(tok, cfg, now) {
		return tok.credentials(), nil
	}
	if f := entry.failure; f != nil && now.Before(f.retryAfter) {
		return nil, f.err
	}

	// WithoutCancel: the caller that happens to trigger a refresh may hang up,
	// but the refresh result serves every waiter. Killing it on one client's
	// disconnect would turn one cancellation into many failures.
	parent := context.Background()
	if ctx != nil {
		parent = context.WithoutCancel(ctx)
	}
	exchangeCtx, cancel := context.WithTimeout(parent, exchangeTimeout)
	defer cancel()

	tok, bErr := exchangeRefreshToken(exchangeCtx, client, resolveClientID(cfg), entry.liveRefreshToken(key), resolveAccountID(cfg))
	if bErr != nil {
		entry.failure = &tokenFailure{
			err:        bErr,
			retryAfter: time.Now().Add(backoffFor(failureAttempts(entry), isPermanentError(bErr))),
			attempts:   failureAttempts(entry) + 1,
		}
		return nil, bErr
	}
	entry.token = tok
	entry.failure = nil
	return tok.credentials(), nil
}

// accountFresh reports whether the cached account binding is still usable: an
// explicit AccountID never goes stale, a discovered one lives for accountTTL.
func accountFresh(tok *codexToken, cfg *schemas.CodexKeyConfig, now time.Time) bool {
	if strings.TrimSpace(cfg.AccountID.GetValue()) != "" {
		return true
	}
	return now.Before(tok.accountAt.Add(accountTTL))
}

// credentials converts a cached token into per-request credentials. An
// explicitly configured AccountID always wins over the discovered one, so an
// operator can pin the workspace without waiting for the cache to age out.
func (t *codexToken) credentials() *codexCredentials {
	return &codexCredentials{
		AccessToken: t.accessToken,
		AccountID:   t.accountID,
		PlanType:    t.planType,
	}
}

// liveRefreshToken returns the refresh token to mint with: the adopted
// rotated value when one exists, otherwise the configured one.
func (e *codexTokenEntry) liveRefreshToken(key schemas.Key) string {
	if e.token != nil && e.token.refreshToken != "" {
		return e.token.refreshToken
	}
	if key.CodexKeyConfig != nil {
		return strings.TrimSpace(key.CodexKeyConfig.RefreshToken.GetValue())
	}
	return ""
}

func loadOrCreateEntry(cacheKey string) *codexTokenEntry {
	if existing, ok := codexTokenPool.Load(cacheKey); ok {
		return existing.(*codexTokenEntry)
	}
	actual, _ := codexTokenPool.LoadOrStore(cacheKey, &codexTokenEntry{})
	return actual.(*codexTokenEntry)
}

func failureAttempts(entry *codexTokenEntry) int {
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
	if key == nil {
		return
	}
	if entry, ok := codexTokenPool.Load(codexCacheKey(*key)); ok {
		e := entry.(*codexTokenEntry)
		e.mu.Lock()
		e.token = nil
		e.mu.Unlock()
	}
}

// resolveClientID returns the OAuth client ID for the exchange: the key's
// configured value, or the built-in Codex CLI client.
func resolveClientID(cfg *schemas.CodexKeyConfig) string {
	if cfg != nil {
		if id := strings.TrimSpace(cfg.ClientID.GetValue()); id != "" {
			return id
		}
	}
	return DefaultOAuthClientID
}

// resolveAccountID returns the explicitly configured workspace, or "" when
// the account must be discovered from wham/accounts/check.
func resolveAccountID(cfg *schemas.CodexKeyConfig) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.AccountID.GetValue())
}

// exchangeRefreshToken performs the OAuth refresh: refresh token in, access
// token, rotated refresh token and account binding out. oauthBase is the
// handler test hook ("" = production URLs); auth.go's ExchangeAuthCodePKCE
// passes it through.
func exchangeRefreshToken(
	ctx context.Context,
	client *fasthttp.Client,
	clientID, refreshToken, accountID string,
) (*codexToken, *schemas.BifrostError) {
	return exchangeRefreshTokenWithBase(ctx, client, clientID, refreshToken, accountID, "")
}

// exchangeRefreshTokenWithBase is exchangeRefreshToken with an overridable
// endpoint base for tests.
func exchangeRefreshTokenWithBase(
	ctx context.Context,
	client *fasthttp.Client,
	clientID, refreshToken, accountID, oauthBase string,
) (*codexToken, *schemas.BifrostError) {
	body, err := sonic.Marshal(map[string]string{
		"client_id":     clientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, configurationError("codex: could not build the token refresh request: " + err.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(oauthTokenURL(oauthBase))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	applyAuthIdentity(req)
	req.SetBody(body)

	if err := doExchangeRequest(ctx, client, req, resp); err != nil {
		return nil, providerUtils.NewProviderAPIError("codex: could not reach OpenAI to refresh the OAuth token", err, 0, nil, nil)
	}

	if status := resp.StatusCode(); status != http.StatusOK {
		return nil, classifyRefreshError(status, resp.Body())
	}

	if len(resp.Body()) > maxExchangeBodyBytes {
		return nil, providerUtils.NewProviderAPIError("codex: token refresh response exceeded size limit", nil, resp.StatusCode(), nil, nil)
	}

	var parsed CodexTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, providerUtils.NewProviderAPIError("codex: could not parse the token refresh response", err, resp.StatusCode(), nil, nil)
	}
	if parsed.AccessToken == "" {
		return nil, blockingError("codex: OpenAI refused the refresh token", resp.StatusCode())
	}

	expiresAt := time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	if parsed.ExpiresIn <= 0 {
		expiresAt = time.Now().Add(time.Hour)
	}
	if !expiresAt.After(time.Now()) {
		return nil, blockingError("codex: OpenAI returned an already-expired access token", resp.StatusCode())
	}
	if expiresAt.Sub(time.Now()) > maxTokenLifetime {
		return nil, blockingError("codex: OpenAI returned an implausible access token expiry", resp.StatusCode())
	}

	tok := &codexToken{
		accessToken:  parsed.AccessToken,
		expiresAt:    expiresAt,
		refreshToken: parsed.RefreshToken,
		accountID:    accountID,
		accountAt:    time.Now(),
	}
	if tok.refreshToken == "" {
		tok.refreshToken = refreshToken
	}

	// No explicit workspace: discover it once. Discovery is fail-soft — a
	// missing binding only drops the chatgpt-account-id header, it never
	// blocks inference.
	if tok.accountID == "" {
		if discovered, plan := discoverAccountWithBase(ctx, client, tok.accessToken, oauthBase); discovered != "" {
			tok.accountID = discovered
			tok.planType = plan
		}
	} else if plan := lookupPlanWithBase(ctx, client, tok.accessToken, tok.accountID, oauthBase); plan != "" {
		tok.planType = plan
	}

	return tok, nil
}

// discoverAccount resolves (accountID, planType) from wham/accounts/check:
// default_account_id wins, then the first account in account_ordering, then
// the first account in accounts.
func discoverAccount(ctx context.Context, client *fasthttp.Client, accessToken string) (string, string) {
	return discoverAccountWithBase(ctx, client, accessToken, "")
}

// discoverAccountWithBase is discoverAccount with an overridable endpoint
// base for tests.
func discoverAccountWithBase(ctx context.Context, client *fasthttp.Client, accessToken, oauthBase string) (string, string) {
	accounts, bErr := fetchAccountsWithBase(ctx, client, accessToken, oauthBase)
	if bErr != nil || accounts == nil || len(accounts.Accounts) == 0 {
		return "", ""
	}
	byID := make(map[string]CodexAccount, len(accounts.Accounts))
	for _, a := range accounts.Accounts {
		byID[a.ID] = a
	}
	for _, id := range []string{accounts.DefaultAccountID} {
		if a, ok := byID[id]; ok && a.ID != "" {
			return a.ID, strings.ToLower(strings.TrimSpace(a.PlanType))
		}
	}
	for _, id := range accounts.AccountOrdering {
		if a, ok := byID[id]; ok && a.ID != "" {
			return a.ID, strings.ToLower(strings.TrimSpace(a.PlanType))
		}
	}
	first := accounts.Accounts[0]
	return first.ID, strings.ToLower(strings.TrimSpace(first.PlanType))
}

// lookupPlan returns the plan_type for a pinned account ID, or "" when the
// account is unknown or the lookup fails. Fail-soft like discovery.
func lookupPlan(ctx context.Context, client *fasthttp.Client, accessToken, accountID string) string {
	return lookupPlanWithBase(ctx, client, accessToken, accountID, "")
}

// lookupPlanWithBase is lookupPlan with an overridable endpoint base for tests.
func lookupPlanWithBase(ctx context.Context, client *fasthttp.Client, accessToken, accountID, oauthBase string) string {
	accounts, bErr := fetchAccountsWithBase(ctx, client, accessToken, oauthBase)
	if bErr != nil || accounts == nil {
		return ""
	}
	for _, a := range accounts.Accounts {
		if a.ID == accountID {
			return strings.ToLower(strings.TrimSpace(a.PlanType))
		}
	}
	return ""
}

// fetchAccounts performs one GET /wham/accounts/check. The endpoint needs no
// chatgpt-account-id header — it is what reports the account IDs.
func fetchAccounts(ctx context.Context, client *fasthttp.Client, accessToken string) (*CodexAccountsCheckResponse, *schemas.BifrostError) {
	return fetchAccountsWithBase(ctx, client, accessToken, "")
}

// fetchAccountsWithBase is fetchAccounts with an overridable endpoint base
// for tests.
func fetchAccountsWithBase(ctx context.Context, client *fasthttp.Client, accessToken, oauthBase string) (*CodexAccountsCheckResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(oauthAccountsURL(oauthBase))
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	applyBackendIdentity(req)

	if err := doExchangeRequest(ctx, client, req, resp); err != nil {
		return nil, providerUtils.NewProviderAPIError("codex: could not reach OpenAI to discover the account", err, 0, nil, nil)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, providerUtils.NewProviderAPIError(
			fmt.Sprintf("codex: account discovery failed (%d)", resp.StatusCode()), nil, resp.StatusCode(), nil, nil)
	}

	if len(resp.Body()) > maxExchangeBodyBytes {
		return nil, providerUtils.NewProviderAPIError("codex: account discovery response exceeded size limit", nil, resp.StatusCode(), nil, nil)
	}

	var parsed CodexAccountsCheckResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, providerUtils.NewProviderAPIError("codex: could not parse the account discovery response", err, resp.StatusCode(), nil, nil)
	}
	return &parsed, nil
}

// doExchangeRequest issues one auth/account exchange request, honouring the
// context deadline.
func doExchangeRequest(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) error {
	if deadline, ok := ctx.Deadline(); ok {
		return client.DoDeadline(req, resp, deadline)
	}
	return client.DoTimeout(req, resp, exchangeTimeout)
}

// classifyRefreshError maps a refresh failure. A failure here is about the
// stored refresh token, never about the request that triggered it. A reused
// or rejected rotating token is permanent: retrying the same dead token, or
// falling back with it, can never succeed.
func classifyRefreshError(status int, body []byte) *schemas.BifrostError {
	detail := codexErrorDetail(body)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return blockingError(fmt.Sprintf("codex: OpenAI rejected the OAuth refresh token (%d). Complete the PKCE flow again and update codex_key_config.refresh_token. %s", status, detail), status)
	default:
		return providerUtils.NewProviderAPIError(
			fmt.Sprintf("codex: token refresh failed (%d). %s", status, detail), nil, status, nil, nil)
	}
}

// applyAuthIdentity stamps the credential-face identity: only originator and
// User-Agent, no Version header (that gate lives on the chatgpt.com face).
func applyAuthIdentity(req *fasthttp.Request) {
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("originator", DefaultOriginator)
}

// applyBackendIdentity stamps the chatgpt.com-face identity for server-side
// account calls.
func applyBackendIdentity(req *fasthttp.Request) {
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("originator", DefaultOriginator)
	req.Header.Set("Version", DefaultVersion)
}

// configurationError builds a setup-fault error that must not drain onto a
// fallback provider.
func configurationError(message string) *schemas.BifrostError {
	bErr := providerUtils.NewConfigurationError(message)
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
