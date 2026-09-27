package cline

import (
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// clineChatResponseHandler parses a Cline non-streaming chat completion after
// unwrapping its envelope. It stands in for the shared handler's default
// parse; raw request/response capture is preserved with the upstream bytes,
// not the unwrapped ones.
func clineChatResponseHandler(responseBody []byte, response *schemas.BifrostChatResponse, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bErr *schemas.BifrostError) {
	inner := responseBody
	var envelope ClineChatEnvelope
	if err := sonic.Unmarshal(responseBody, &envelope); err == nil && len(envelope.Data) > 0 {
		inner = envelope.Data
	}
	rawRequest, _, bErr = providerUtils.HandleProviderResponse(inner, response, requestBody, sendBackRawRequest, false)
	if bErr != nil {
		return nil, nil, bErr
	}
	if sendBackRawResponse {
		rawResponse = compactJSON(responseBody)
	}
	return rawRequest, rawResponse, nil
}

// compactJSON returns the smallest faithful encoding of body, falling back to
// the original bytes when they are not valid JSON.
func compactJSON(body []byte) interface{} {
	var v interface{}
	if err := sonic.Unmarshal(body, &v); err != nil {
		return string(body)
	}
	compacted, err := sonic.Marshal(v)
	if err != nil {
		return string(body)
	}
	return string(compacted)
}
