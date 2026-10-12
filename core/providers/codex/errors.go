package codex

import (
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// parseCodexError converts a Codex inference error into a BifrostError.
//
// Codex auth failures are configuration faults rather than transient ones: a
// revoked token or a dead refresh family improves by re-authenticating, never
// by retrying elsewhere. 401 and 403 therefore block fallbacks; rate limits
// and server errors are left alone.
func parseCodexError(resp *fasthttp.Response) *schemas.BifrostError {
	var fallback any
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &fallback)
	if bifrostErr == nil {
		bifrostErr = &schemas.BifrostError{}
	}
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	if body := resp.Body(); len(body) > 0 {
		if errVal := gjson.GetBytes(body, "error"); errVal.Exists() {
			switch {
			case errVal.Type == gjson.String && errVal.String() != "":
				bifrostErr.Error.Message = errVal.String()
			case errVal.IsObject():
				if msg := errVal.Get("message").String(); msg != "" {
					bifrostErr.Error.Message = msg
				}
				if typ := errVal.Get("type").String(); typ != "" {
					bifrostErr.Error.Type = &typ
				} else if code := errVal.Get("code").String(); code != "" {
					bifrostErr.Error.Code = &code
				}
			}
		} else if msg := gjson.GetBytes(body, "message").String(); msg != "" {
			bifrostErr.Error.Message = msg
		}
	}

	switch resp.StatusCode() {
	case fasthttp.StatusUnauthorized:
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
		bifrostErr.Error.Message = "codex: the access token was rejected (401). " +
			"It expired or was revoked; OAuth keys mint a fresh one automatically. Upstream said: " + upstreamDetail(bifrostErr.Error.Message)
	case fasthttp.StatusForbidden:
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
		bifrostErr.Error.Message = "codex: OpenAI refused the request (403). " +
			"The account may lack access to this model or workspace. Upstream said: " + upstreamDetail(bifrostErr.Error.Message)
	}

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = "codex: provider API error (status " + itoa(*bifrostErr.StatusCode) + ")"
		} else {
			bifrostErr.Error.Message = "codex: provider API error"
		}
	}

	return bifrostErr
}

// codexErrorDetail extracts the human-readable error text from an OAuth
// token-endpoint failure body, which uses the OpenAI {"error": {...}} shape.
func codexErrorDetail(body []byte) string {
	if len(body) == 0 {
		return "(no detail)"
	}
	if errVal := gjson.GetBytes(body, "error"); errVal.Exists() {
		if errVal.Type == gjson.String && errVal.String() != "" {
			return errVal.String()
		}
		if errVal.IsObject() {
			if msg := errVal.Get("message").String(); msg != "" {
				if code := errVal.Get("code").String(); code != "" {
					return msg + " (code: " + code + ")"
				}
				return msg
			}
		}
	}
	var raw any
	if err := sonic.Unmarshal(body, &raw); err == nil {
		if compacted, err := sonic.Marshal(raw); err == nil && len(compacted) > 0 {
			return string(compacted)
		}
	}
	return "(no detail)"
}

// upstreamDetail keeps the operator-facing message readable when Codex
// returns an empty body.
func upstreamDetail(message string) string {
	if strings.TrimSpace(message) == "" {
		return "(no detail)"
	}
	return message
}
