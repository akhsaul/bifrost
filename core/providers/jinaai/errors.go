package jinaai

import (
	"fmt"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseJinaError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp JinaErrorResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	msg := ""
	switch d := errorResp.Detail.(type) {
	case string:
		msg = d
	case []interface{}:
		if len(d) > 0 {
			if b, err := sonic.Marshal(d); err == nil {
				msg = string(b)
			}
		}
	}

	if msg == "" && errorResp.Message != nil {
		msg = *errorResp.Message
	}

	if msg == "" && len(errorResp.Errors) > 0 {
		msg = fmt.Sprintf("%s: %s", errorResp.Errors[0].Field, errorResp.Errors[0].Message)
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	if msg != "" {
		bifrostErr.Error.Message = msg
	}

	if errorResp.Code != nil {
		bifrostErr.Error.Code = errorResp.Code
	}

	if errorResp.RequestID != nil {
		bifrostErr.EventID = errorResp.RequestID
	}

	bifrostErr.ExtraFields.Provider = schemas.JinaAI
	return bifrostErr
}
