package cline

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// parseClineError parses Cline API error responses into a BifrostError.
// Cline reports failures as {"error": ..., "success": false} where error is
// either a string or an object carrying a message.
func parseClineError(resp *fasthttp.Response) *schemas.BifrostError {
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
					bifrostErr.Error.Type = &code
				}
			}
		} else if msg := gjson.GetBytes(body, "message").String(); msg != "" {
			bifrostErr.Error.Message = msg
		}
	}

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("cline: provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "cline: provider API error"
		}
	}

	return bifrostErr
}
