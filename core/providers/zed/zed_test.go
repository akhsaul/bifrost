package zed_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/zed"
	"github.com/maximhq/bifrost/core/schemas"
)

func testContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func TestBuildHeadersCompletions(t *testing.T) {
	headers := zed.BuildHeaders(testContext(), nil, true)
	for _, k := range []string{"user-agent", "x-zed-version", "x-zed-client-supports-status-messages", "x-zed-client-supports-stream-ended-request-completion-status"} {
		if _, ok := headers[k]; !ok {
			t.Errorf("missing header %q", k)
		}
	}
	if headers["user-agent"] != zed.DefaultUserAgent {
		t.Errorf("user-agent = %q, want %q", headers["user-agent"], zed.DefaultUserAgent)
	}
	if _, ok := headers["authorization"]; ok {
		t.Error("authorization must never be set by BuildHeaders")
	}
}

func TestBuildHeadersUserAuth(t *testing.T) {
	headers := zed.BuildHeaders(testContext(), nil, false)
	if _, ok := headers["x-zed-system-id"]; !ok {
		t.Error("user-auth headers must include x-zed-system-id")
	}
	if _, ok := headers["x-zed-version"]; ok {
		t.Error("user-auth headers must not include x-zed-version")
	}
}

func TestBuildHeadersOverrides(t *testing.T) {
	headers := zed.BuildHeaders(nil, map[string]string{"User-Agent": "custom/1.0", "authorization": "evil"}, true)
	if headers["User-Agent"] != "custom/1.0" {
		t.Errorf("config user-agent = %q, want custom/1.0", headers["User-Agent"])
	}
	for k := range headers {
		if strings.EqualFold(k, "authorization") {
			t.Fatal("config must not inject authorization via BuildHeaders")
		}
	}
}

func TestUnsupportedOperations(t *testing.T) {
	provider, err := zed.NewZedProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, bErr := provider.Embedding(testContext(), schemas.Key{}, &schemas.BifrostEmbeddingRequest{}); bErr == nil {
		t.Error("Embedding should be unsupported")
	}
	if _, bErr := provider.TextCompletion(testContext(), schemas.Key{}, &schemas.BifrostTextCompletionRequest{}); bErr == nil {
		t.Error("TextCompletion should be unsupported")
	}
	if _, bErr := provider.CountTokens(testContext(), schemas.Key{}, &schemas.BifrostResponsesRequest{}); bErr == nil {
		t.Error("CountTokens should be unsupported")
	}
}

// TestStartLoginURLShape pins the editor's native_app_signin contract:
// signin lives on the base domain (zed.dev, NOT cloud.zed.dev), the query
// carries native_app_port + native_app_public_key, and there is no
// redirect_uri — the editor binds 127.0.0.1:0 and hands Zed only the port.
func TestStartLoginURLShape(t *testing.T) {
	challenge, bErr := zed.StartLogin(testContext(), "", "")
	if bErr != nil {
		t.Fatalf("StartLogin: %v", bErr)
	}
	if challenge == nil || challenge.LoginURL == "" || challenge.SessionID == "" {
		t.Fatal("StartLogin must return session_id + login_url")
	}
	u, err := url.Parse(challenge.LoginURL)
	if err != nil {
		t.Fatalf("LoginURL does not parse: %v", err)
	}
	if u.Host != "zed.dev" {
		t.Errorf("LoginURL host = %q, want zed.dev (auth domain, not cloud.zed.dev)", u.Host)
	}
	if u.Path != "/native_app_signin" {
		t.Errorf("LoginURL path = %q, want /native_app_signin", u.Path)
	}
	q := u.Query()
	if q.Get("native_app_port") == "" {
		t.Error("LoginURL must carry native_app_port (the loopback listener port)")
	}
	if q.Get("native_app_public_key") == "" {
		t.Error("LoginURL must carry native_app_public_key")
	}
	if _, present := q["redirect_uri"]; present {
		t.Error("LoginURL must NOT carry redirect_uri (editor sends port only)")
	}
	// The public key must be BASE64_URL_SAFE(PKCS1 DER) decodable (padded,
	// like the editor's rpc auth TryFrom<PublicKey>).
	der, err := base64.URLEncoding.DecodeString(q.Get("native_app_public_key"))
	if err != nil || len(der) == 0 {
		t.Fatalf("native_app_public_key is not base64url: %v", err)
	}
	if _, err := x509.ParsePKCS1PublicKey(der); err != nil {
		t.Errorf("native_app_public_key is not a PKCS1 DER RSA key: %v", err)
	}
	// Polling a fresh session before the browser lands must be pending.
	res, bErr := zed.PollLogin(testContext(), challenge.SessionID)
	if bErr != nil {
		t.Fatalf("PollLogin: %v", bErr)
	}
	if res.Status != zed.LoginPollPending {
		t.Errorf("fresh login poll = %q, want pending", res.Status)
	}
}

// TestVerifyLoginAgainstUsersMe pins the OAuth completion step: the freshly
// decrypted (user_id, access_token) pair is verified once against
// GET /client/users/me, and the poll result carries the username +
// organization_id the key form needs to save safely.
func TestVerifyLoginAgainstUsersMe(t *testing.T) {
	const userID = "678672"
	const blob = `{"version":2,"id":"client_token_abc","token":"tok_xyz"}`
	stub := zed.NewUsersMeStub(t,
		userID+" "+blob,
		`{"user":{"id":678672,"username":"akhsaul"},"default_organization_id":"org_01test"}`,
		200,
	)
	username, orgID, bErr := zed.VerifyLoginForTest(stub, "", userID, blob)
	if bErr != nil {
		t.Fatalf("VerifyLoginForTest: %v", bErr)
	}
	if username != "akhsaul" {
		t.Errorf("username = %q, want akhsaul", username)
	}
	if orgID != "org_01test" {
		t.Errorf("organization_id = %q, want org_01test", orgID)
	}
}

// TestVerifyLoginRejectsBadCredentials pins the failure side: a users/me
// rejection must surface as an error, never as an empty success the form
// would then try to save.
func TestVerifyLoginRejectsBadCredentials(t *testing.T) {
	stub := zed.NewUsersMeStub(t,
		"678672 right-blob",
		`{}`,
		200,
	)
	_, _, bErr := zed.VerifyLoginForTest(stub, "", "678672", "wrong-blob")
	if bErr == nil {
		t.Fatal("VerifyLoginForTest with a rejected credential must return an error")
	}
}
