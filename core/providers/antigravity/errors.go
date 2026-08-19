package antigravity

import (
	"fmt"
	"strconv"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseAntigravityError parses Google Cloud Code error responses into BifrostError.
func parseAntigravityError(resp *fasthttp.Response, model string) *schemas.BifrostError {
	statusCode := resp.StatusCode()
	body := resp.Body()

	var errPayload struct {
		Error *AntigravityErrorPayload `json:"error"`
	}

	bifrostErr := &schemas.BifrostError{
		StatusCode:     schemas.Ptr(statusCode),
		IsBifrostError: false,
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:               schemas.Antigravity,
			OriginalModelRequested: model,
			RequestType:            schemas.ChatCompletionRequest,
		},
		Error: &schemas.ErrorField{
			Code:    schemas.Ptr(strconv.Itoa(statusCode)),
			Message: fmt.Sprintf("Antigravity API returned status %d", statusCode),
		},
	}

	if len(body) > 0 {
		if err := sonic.Unmarshal(body, &errPayload); err == nil && errPayload.Error != nil {
			if errPayload.Error.Message != "" {
				bifrostErr.Error.Message = errPayload.Error.Message
			}
			if errPayload.Error.Status != "" {
				bifrostErr.Error.Type = schemas.Ptr(errPayload.Error.Status)
			}
			if errPayload.Error.Code != 0 {
				bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(errPayload.Error.Code))
			}
		} else {
			bifrostErr.Error.Message = string(body)
		}
	}

	// Classify standard HTTP statuses
	switch statusCode {
	case 400:
		if bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr("invalid_request_error")
		}
	case 401:
		if bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr("authentication_error")
		}
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
	case 403:
		if bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr("permission_denied")
		}
		bifrostErr.AllowFallbacks = schemas.Ptr(false)
	case 404:
		if bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr("not_found_error")
		}
	case 422:
		if bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr("unprocessable_entity")
		}
	case 429:
		if bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr("rate_limit_exceeded")
		}
		bifrostErr.AllowFallbacks = schemas.Ptr(true)
	case 500, 502, 503, 504:
		if bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr("api_error")
		}
		bifrostErr.AllowFallbacks = schemas.Ptr(true)
	}

	return bifrostErr
}

// newAuthenticationError creates a BifrostError for authentication failures.
func newAuthenticationError(message string, err error) *schemas.BifrostError {
	statusCode := 401
	errType := "authentication_error"
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		AllowFallbacks: schemas.Ptr(false),
		Error: &schemas.ErrorField{
			Message: message,
			Type:    &errType,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: schemas.Antigravity,
		},
	}
}
