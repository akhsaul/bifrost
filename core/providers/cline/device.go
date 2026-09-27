package cline

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// WorkOS device-flow endpoints. Cline authenticates through WorkOS; the
// device flow is the only browserless grant WorkOS offers, and WorkOS sends
// no callback when the user approves, so the caller must poll.
const (
	DefaultWorkOSBaseURL = "https://api.workos.com"
	workOSDevicePath     = "/user_management/authorize/device"
	workOSAuthPath       = "/user_management/authenticate"

	// deviceGrantType is the RFC 8628 device_code grant identifier.
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
)

// ClineDeviceChallenge is one WorkOS device authorization: show the user code
// and verification URI, then poll with the device code until it resolves.
type ClineDeviceChallenge struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// ClineDevicePollResult is the outcome of one poll attempt.
type ClineDevicePollResult struct {
	Status       string `json:"status"` // pending | success | expired | denied | error
	RefreshToken string `json:"refresh_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	Email        string `json:"email,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Poll statuses returned by PollDeviceAuth.
const (
	DevicePollPending = "pending"
	DevicePollSuccess = "success"
	DevicePollExpired = "expired"
	DevicePollDenied  = "denied"
	DevicePollError   = "error"
)

// deviceClientOnce guards the shared WorkOS client below.
var deviceClientOnce sync.Once
var sharedDeviceClient *fasthttp.Client

// DeviceFlowClient returns the shared HTTP client for WorkOS device-flow
// calls. The device flow is setup-time traffic against a single host, so one
// pooled client serves every authorize/poll call instead of handshaking per
// poll.
func DeviceFlowClient() *fasthttp.Client {
	deviceClientOnce.Do(func() {
		timeout := 30 * time.Second
		sharedDeviceClient = &fasthttp.Client{
			ReadTimeout:         timeout,
			WriteTimeout:        timeout,
			MaxConnsPerHost:     20,
			MaxIdleConnDuration: 30 * time.Second,
			MaxConnWaitTimeout:  timeout,
			ConnPoolStrategy:    fasthttp.FIFO,
		}
	})
	return sharedDeviceClient
}

// workOSBaseURL resolves the WorkOS base URL, defaulting to production.
// An explicit base exists so tests can point the flow at a mock server.
func workOSBaseURL(baseURL string) string {
	if strings.TrimSpace(baseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	return DefaultWorkOSBaseURL
}

// resolveDeviceClientID returns the WorkOS client ID for the device flow:
// the key's configured value, or the built-in Cline client.
func resolveDeviceClientID(key *schemas.Key) string {
	if key != nil && key.ClineKeyConfig != nil {
		if id := strings.TrimSpace(key.ClineKeyConfig.ClientID.GetValue()); id != "" {
			return id
		}
	}
	return DefaultWorkOSClientID
}

// StartDeviceFlow initiates a WorkOS device authorization. The caller shows
// VerificationURIComplete/UserCode to the operator and polls PollDeviceAuth
// with DeviceCode until it resolves or ExpiresIn elapses.
func StartDeviceFlow(ctx context.Context, client *fasthttp.Client, clientID, workosBase string) (*ClineDeviceChallenge, *schemas.BifrostError) {
	if strings.TrimSpace(clientID) == "" {
		clientID = DefaultWorkOSClientID
	}

	form := url.Values{}
	form.Set("client_id", clientID)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(workOSBaseURL(workosBase) + workOSDevicePath)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.SetUserAgent("node")
	req.SetBodyString(form.Encode())

	if err := doExchangeRequest(ctx, client, req, resp); err != nil {
		return nil, providerUtils.NewProviderAPIError("cline: could not reach WorkOS to start the device flow", err, 0, nil, nil)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, providerUtils.NewProviderAPIError(
			fmt.Sprintf("cline: WorkOS rejected the device authorization request (%d)", resp.StatusCode()),
			nil, resp.StatusCode(), nil, nil)
	}

	var challenge ClineDeviceChallenge
	if err := sonic.Unmarshal(resp.Body(), &challenge); err != nil {
		return nil, providerUtils.NewProviderAPIError("cline: could not parse WorkOS device authorization response", err, resp.StatusCode(), nil, nil)
	}
	if challenge.DeviceCode == "" || challenge.VerificationURIComplete == "" {
		return nil, providerUtils.NewProviderAPIError("cline: WorkOS returned an incomplete device authorization", nil, resp.StatusCode(), nil, nil)
	}
	return &challenge, nil
}

// PollDeviceAuth performs exactly one WorkOS authenticate attempt for a
// device code. It is stateless by design: the caller (the UI) owns the 1s
// cadence and stops on any non-pending status or expiry.
func PollDeviceAuth(ctx context.Context, client *fasthttp.Client, clientID, deviceCode, workosBase string) (*ClineDevicePollResult, *schemas.BifrostError) {
	if strings.TrimSpace(clientID) == "" {
		clientID = DefaultWorkOSClientID
	}
	if strings.TrimSpace(deviceCode) == "" {
		return nil, configurationError("cline: device_code is required to poll the device flow")
	}

	form := url.Values{}
	form.Set("grant_type", deviceGrantType)
	form.Set("device_code", deviceCode)
	form.Set("client_id", clientID)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(workOSBaseURL(workosBase) + workOSAuthPath)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.SetUserAgent("node")
	req.SetBodyString(form.Encode())

	if err := doExchangeRequest(ctx, client, req, resp); err != nil {
		return nil, providerUtils.NewProviderAPIError("cline: could not reach WorkOS to poll the device flow", err, 0, nil, nil)
	}

	// WorkOS answers pending polls with 4xx carrying an OAuth error code, and
	// success with 200. Both shapes are handled below; only transport failures
	// stay Bifrost errors. Tokens are read out of the body and never logged.
	if resp.StatusCode() == http.StatusOK {
		return parseDeviceSuccess(resp.Body())
	}
	return parseDevicePollError(resp.Body(), resp.StatusCode()), nil
}

// deviceSuccessBody is the approved-poll wire shape. Only the fields Bifrost
// needs are decoded; the rest (notably userInfo) is deliberately ignored.
type deviceSuccessBody struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         struct {
		Email string `json:"email"`
	} `json:"user"`
}

// parseDeviceSuccess converts an approved poll into tokens. A missing refresh
// token is a hard failure: without it there is nothing durable to store.
func parseDeviceSuccess(body []byte) (*ClineDevicePollResult, *schemas.BifrostError) {
	var parsed deviceSuccessBody
	if err := sonic.Unmarshal(body, &parsed); err != nil {
		return nil, providerUtils.NewProviderAPIError("cline: could not parse WorkOS approval response", err, http.StatusOK, nil, nil)
	}
	if parsed.RefreshToken == "" {
		return nil, providerUtils.NewProviderAPIError("cline: WorkOS approved the flow but returned no refresh token", nil, http.StatusOK, nil, nil)
	}
	return &ClineDevicePollResult{
		Status:       DevicePollSuccess,
		RefreshToken: parsed.RefreshToken,
		AccessToken:  parsed.AccessToken,
		Email:        parsed.User.Email,
	}, nil
}

// deviceErrorBody is the pending/failed-poll wire shape.
type deviceErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// parseDevicePollError maps a non-200 poll to a status the UI can act on:
// pending keeps polling, everything else stops it with a message.
func parseDevicePollError(body []byte, status int) *ClineDevicePollResult {
	var parsed deviceErrorBody
	_ = sonic.Unmarshal(body, &parsed)
	switch parsed.Error {
	case "authorization_pending":
		return &ClineDevicePollResult{Status: DevicePollPending}
	case "slow_down":
		return &ClineDevicePollResult{Status: DevicePollPending, Message: "WorkOS asked to slow down polling"}
	case "expired_token":
		return &ClineDevicePollResult{Status: DevicePollExpired, Message: "The device code expired. Start over to get a new one."}
	case "access_denied":
		return &ClineDevicePollResult{Status: DevicePollDenied, Message: "The authorization was denied."}
	case "":
		return &ClineDevicePollResult{Status: DevicePollError, Message: fmt.Sprintf("WorkOS device poll failed (status %d)", status)}
	default:
		msg := parsed.ErrorDescription
		if msg == "" {
			msg = parsed.Error
		}
		return &ClineDevicePollResult{Status: DevicePollError, Message: msg}
	}
}

// doExchangeRequest issues one WorkOS exchange request, honouring the context
// deadline. Poll cadence is owned by the caller; this only bounds a single
// attempt.
func doExchangeRequest(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) error {
	if deadline, ok := ctx.Deadline(); ok {
		return client.DoDeadline(req, resp, deadline)
	}
	return client.DoTimeout(req, resp, refreshTimeout)
}
