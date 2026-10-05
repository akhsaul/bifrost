package zed

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Zed browser login mirrors the native editor flow (crates/client
// authenticate_with_browser + crates/rpc auth): a fresh RSA-2048 keypair per
// attempt, an ephemeral loopback listener on 127.0.0.1:0, and a browser
// redirect carrying user_id + access_token encrypted to the public key.
//
//  1. StartLogin generates a keypair, opens 127.0.0.1:0, and returns the URL
//     the operator opens: https://zed.dev/native_app_signin with
//     native_app_port + native_app_public_key (BASE64_URL_SAFE of the PKCS1
//     DER) + optional system_id. The session lives 5 minutes.
//  2. The operator signs in; Zed redirects to
//     http://127.0.0.1:<port>/?user_id=<id>&access_token=<encrypted>.
//  3. PollLogin returns pending until the redirect lands, then decrypts
//     (OAEP-SHA256, fallback PKCS1v15 for older servers) and hands back the
//     verbatim access_token blob for zed_key_config.
//
// Auth lives on the base domain (zed.dev); the cloud.* host is only the API
// surface. Remote Bifrosts cannot receive the loopback redirect, so the UI
// also offers manual paste (user_id + access_token fields on the key).

// ZedLoginChallenge is one browser login attempt: show LoginURL to the
// operator, then poll with SessionID until it resolves.
type ZedLoginChallenge struct {
	SessionID string `json:"session_id"`
	LoginURL  string `json:"login_url"`
	ExpiresIn int    `json:"expires_in"`
}

// ZedLoginPollResult is the outcome of one poll attempt.
type ZedLoginPollResult struct {
	Status      string `json:"status"` // pending | success | expired | error
	UserID      string `json:"user_id,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	Message     string `json:"message,omitempty"`
}

// Poll statuses returned by PollLogin.
const (
	LoginPollPending = "pending"
	LoginPollSuccess = "success"
	LoginPollExpired = "expired"
	LoginPollError   = "error"
)

const (
	// loginSessionTTL bounds one browser login attempt.
	loginSessionTTL = 5 * time.Minute
	// loginListenerTimeout bounds the loopback listener lifetime.
	loginListenerTimeout = 5 * time.Minute
	// zedSigninPath is the login page that accepts the public key.
	zedSigninPath = "/native_app_signin"
	// signinSucceededPath is the hosted confirmation page the loopback
	// listener 302-redirects the browser to after storing the callback
	// (mirrors crates/client authenticate_with_browser).
	signinSucceededPath = "/native_app_signin_succeeded"
	// DefaultSigninBaseURL is the base domain that serves the browser login.
	// The editor's default server_url is https://zed.dev; signin is a page on
	// that host, not on the cloud.* API surface used for inference.
	DefaultSigninBaseURL = "https://zed.dev"
)

// zedLoginSession holds one in-flight login: the private key for decryption
// and the redirect payload once the browser lands.
type zedLoginSession struct {
	privateKey *rsa.PrivateKey
	createdAt  time.Time
	listener   net.Listener
	userID     string
	blob       string
	done       bool
	mu         sync.Mutex
}

// zedLoginPool maps session id to *zedLoginSession.
var zedLoginPool sync.Map

// StartLogin begins one browser login attempt. It generates a fresh RSA-2048
// keypair, serves an ephemeral loopback listener for the post-login redirect,
// and returns the signin URL the operator opens.
//
// The signin URL mirrors the editor (client.rs authenticate_with_browser):
// https://<host>/native_app_signin?native_app_port=<port>&
// native_app_public_key=<BASE64_URL_SAFE(PKCS1 DER)>. There is NO
// redirect_uri: the editor binds 127.0.0.1:0 and hands Zed only the port,
// because the redirect target is always the same loopback listener.
//
// signinBaseURL selects the login host (https://zed.dev by default, the
// editor's default server_url); systemID is forwarded as system_id when set.
// Pass "" for either to use the default / omit it.
func StartLogin(ctx context.Context, signinBaseURL string, systemID string) (*ZedLoginChallenge, *schemas.BifrostError) {
	base := strings.TrimRight(strings.TrimSpace(signinBaseURL), "/")
	if base == "" {
		base = DefaultSigninBaseURL
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, providerUtils.NewProviderAPIError("zed: could not generate the login keypair", err, 0, nil, nil)
	}
	der := x509.MarshalPKCS1PublicKey(&privateKey.PublicKey)
	// Match the editor exactly (rpc auth TryFrom<PublicKey>: BASE64_URL_SAFE
	// with padding). RSA-2048 PKCS1 DER happens to need no padding today, but
	// padded encoding is what Zed's decoder expects, so send padded.
	publicKey := base64.URLEncoding.EncodeToString(der)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, providerUtils.NewProviderAPIError("zed: could not open the login callback listener (browser login needs loopback access to this Bifrost instance)", err, 0, nil, nil)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	session := &zedLoginSession{privateKey: privateKey, createdAt: time.Now(), listener: listener}
	sessionID := newSessionID()
	zedLoginPool.Store(sessionID, session)

	go serveLoginCallback(sessionID, session, base)

	query := "native_app_port=" + strconv.Itoa(port) +
		"&native_app_public_key=" + url.QueryEscape(publicKey)
	if strings.TrimSpace(systemID) != "" {
		query += "&system_id=" + url.QueryEscape(strings.TrimSpace(systemID))
	}
	loginURL := base + zedSigninPath + "?" + query
	_ = ctx
	return &ZedLoginChallenge{
		SessionID: sessionID,
		LoginURL:  loginURL,
		ExpiresIn: int(loginSessionTTL.Seconds()),
	}, nil
}

// serveLoginCallback serves the single post-login redirect, then closes the
// listener. Only loopback remotes are accepted. It mirrors the editor: the
// callback reads user_id + access_token from the query, stores the STILL
// ENCRYPTED blob, and 302-redirects the browser to the hosted
// /native_app_signin_succeeded page — the "login received" screen the user
// sees comes from zed.dev, not from us.
func serveLoginCallback(sessionID string, session *zedLoginSession, signinBase string) {
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isLoopback(r.RemoteAddr) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			q := r.URL.Query()
			userID := q.Get("user_id")
			blob := q.Get("access_token")
			if userID == "" || blob == "" {
				http.Error(w, "missing user_id or access_token", http.StatusBadRequest)
				return
			}
			session.mu.Lock()
			session.userID = userID
			session.blob = blob
			session.done = true
			session.mu.Unlock()
			// Editor behaviour: 302 to the hosted success page. Decryption
			// happens later, in PollLogin, so a wrong key can never show a
			// false success screen.
			http.Redirect(w, r, strings.TrimRight(signinBase, "/")+signinSucceededPath, http.StatusFound)
		}),
		ReadTimeout: 30 * time.Second,
	}
	go func() {
		time.Sleep(loginListenerTimeout)
		session.listener.Close()
	}()
	_ = server.Serve(session.listener)
}

// isLoopback reports whether the remote address is loopback.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// PollLogin performs exactly one login-session check. It is stateless by
// design: the caller (the UI) owns the 1s cadence and stops on any
// non-pending status or expiry. A success carries the user_id + verbatim
// access_token blob the UI stores on the key; pending carries no credential.
// Decryption failures surface as status=error (HTTP 200) so the UI poll keeps
// a machine-readable signal instead of an HTTP 5xx per-tick retry.
func PollLogin(ctx context.Context, sessionID string) (*ZedLoginPollResult, *schemas.BifrostError) {
	_ = ctx
	if strings.TrimSpace(sessionID) == "" {
		return nil, configurationError("zed: session_id is required to poll the login flow")
	}
	raw, ok := zedLoginPool.Load(sessionID)
	if !ok {
		return &ZedLoginPollResult{Status: LoginPollExpired, Message: "The login session expired. Start over to get a new one."}, nil
	}
	session := raw.(*zedLoginSession)
	if time.Since(session.createdAt) > loginSessionTTL {
		zedLoginPool.Delete(sessionID)
		session.listener.Close()
		return &ZedLoginPollResult{Status: LoginPollExpired, Message: "The login session expired. Start over to get a new one."}, nil
	}
	session.mu.Lock()
	done, userID, blob := session.done, session.userID, session.blob
	session.mu.Unlock()
	if !done {
		return &ZedLoginPollResult{Status: LoginPollPending}, nil
	}
	accessToken, bErr := decryptAccessToken(session.privateKey, blob)
	if bErr != nil {
		zedLoginPool.Delete(sessionID)
		session.listener.Close()
		msg := "Could not decrypt the login token."
		if bErr.Error != nil && bErr.Error.Message != "" {
			msg = bErr.Error.Message
		}
		return &ZedLoginPollResult{Status: LoginPollError, Message: msg}, nil
	}
	zedLoginPool.Delete(sessionID)
	session.listener.Close()
	return &ZedLoginPollResult{Status: LoginPollSuccess, UserID: userID, AccessToken: accessToken}, nil
}

// decryptAccessToken base64url-decodes the redirect blob and decrypts it
// with the session private key: OAEP-SHA256 first, PKCS1v15 fallback for
// older servers. The plaintext is the verbatim access_token JSON blob for
// zed_key_config — never parsed or re-encoded here. It accepts both padded
// and unpadded base64url (BASE64_URL_SAFE per rpc auth), because padding is
// a transport detail the sender may or may not include.
func decryptAccessToken(privateKey *rsa.PrivateKey, blob string) (string, *schemas.BifrostError) {
	trimmed := strings.TrimSpace(blob)
	ciphertext, err := base64.URLEncoding.DecodeString(trimmed)
	if err != nil {
		ciphertext, err = base64.RawURLEncoding.DecodeString(trimmed)
	}
	if err != nil {
		// Not encrypted (manual-paste path hands the blob straight through):
		// treat it as the verbatim token when it already looks like JSON.
		if looksLikeTokenBlob(blob) {
			return strings.TrimSpace(blob), nil
		}
		return "", providerUtils.NewProviderAPIError("zed: could not decode the login access_token", err, 0, nil, nil)
	}
	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, ciphertext, nil)
	if err != nil {
		plaintext, err = rsa.DecryptPKCS1v15(rand.Reader, privateKey, ciphertext)
		if err != nil {
			return "", providerUtils.NewProviderAPIError("zed: could not decrypt the login access_token", err, 0, nil, nil)
		}
	}
	token := string(plaintext)
	if !looksLikeTokenBlob(token) {
		return "", providerUtils.NewProviderAPIError("zed: the login access_token has an unexpected shape", nil, 0, nil, nil)
	}
	return token, nil
}

// looksLikeTokenBlob reports whether s is plausibly the verbatim Zed token
// ({"version":2,"id":"client_token_...","token":"..."}).
func looksLikeTokenBlob(s string) bool {
	var v struct {
		Version *int    `json:"version"`
		ID      *string `json:"id"`
		Token   *string `json:"token"`
	}
	if err := sonic.Unmarshal([]byte(strings.TrimSpace(s)), &v); err != nil {
		return false
	}
	return v.Version != nil && v.ID != nil && v.Token != nil
}

// newSessionID mints a random login session id.
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}

// LoginClient returns the shared HTTP client for login-time calls.
var loginClientOnce sync.Once
var sharedLoginClient *fasthttp.Client

// LoginClient returns the shared HTTP client for Zed login helpers.
func LoginClient() *fasthttp.Client {
	loginClientOnce.Do(func() {
		timeout := 30 * time.Second
		sharedLoginClient = &fasthttp.Client{
			ReadTimeout:         timeout,
			WriteTimeout:        timeout,
			MaxConnsPerHost:     20,
			MaxIdleConnDuration: 30 * time.Second,
			MaxConnWaitTimeout:  timeout,
			ConnPoolStrategy:    fasthttp.FIFO,
		}
	})
	return sharedLoginClient
}
