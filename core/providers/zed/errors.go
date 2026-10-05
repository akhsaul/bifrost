package zed

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// parseZedError parses Zed API error responses into a BifrostError.
// Zed reports failures as {"error": ...} where error is either a string or
// an object carrying a message, mirroring the Cline shape.
func parseZedError(resp *fasthttp.Response) *schemas.BifrostError {
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
		// Zed sometimes wraps the upstream HTTP status in the body.
		if code := gjson.GetBytes(body, "upstream_http_code"); code.Exists() && code.Int() != 0 {
			status := int(code.Int())
			bifrostErr.StatusCode = &status
		}
	}

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("zed: provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "zed: provider API error"
		}
	}

	return bifrostErr
}
