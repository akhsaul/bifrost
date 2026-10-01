package opencodefree

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// opencodeFreeErrorBody represents the JSON error envelope returned by OpenCode Free API.
// Format: {"type": "error", "error": {"type": "...", "message": "..."}}
type opencodeFreeErrorBody struct {
	Type  string                 `json:"type"`
	Error opencodeFreeErrorInner `json:"error"`
}

type opencodeFreeErrorInner struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// parseOpencodeFreeError parses OpenCode-specific error responses.
func parseOpencodeFreeError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorBody opencodeFreeErrorBody
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorBody)
	if bifrostErr == nil {
		bifrostErr = &schemas.BifrostError{}
	}
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	if body := resp.Body(); len(body) > 0 {
		var parsed opencodeFreeErrorBody
		if err := sonic.Unmarshal(body, &parsed); err == nil && parsed.Type == "error" {
			if parsed.Error.Message != "" {
				bifrostErr.Error.Message = parsed.Error.Message
			}
			if parsed.Error.Type != "" {
				bifrostErr.Error.Type = &parsed.Error.Type
			}
		}
	}

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "provider API error"
		}
	}

	return bifrostErr
}
