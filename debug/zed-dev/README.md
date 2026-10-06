# Get organization_id
curl --request GET \
 --url https://cloud.zed.dev/client/users/me \
 --header 'authorization: 678672 {"version":2,"id":"client_token_<redacted-sghr>","token":"<redacted-byxb5jz>"}' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-system-id: 3dfad06a-5c48-4c7b-a480-7ea86b311eb9' > resp-users-me.json
Note:
- if i change number `678672` to another then it will UnAuthorized, looks like the number is tied to identity account.
- `client_token` and `token` is different value.
- looks like `client_token` treated as an `id` and maybe `token` is a real token.
- `client_token` has prefix "client_token_".
- `token` doesn't have any prefix.


----


# Get $API_KEY
curl --request POST \
 --url https://cloud.zed.dev/client/llm_tokens \
 --header 'authorization: 678672 {"version":2,"id":"client_token_<redacted-sghr>","token":"<redacted-byxb5jz>"}' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-system-id: 3dfad06a-5c48-4c7b-a480-7ea86b311eb9' \
 --data '
{
  "organization_id": "org_01km1bf8f68enexw8gag4sv1zn"
}
' > resp-llm-tokens.json
Note:
- `organization_id` value can be found at /client/users/me by using key `default_organization_id` or `organizations[0].id`.
- if i change number `678672` to another then it will UnAuthorized, looks like the number is tied to identity account.
- `client_token` and `token` is different value.
- looks like `client_token` treated as an `id` and maybe `token` is a real token.
- `client_token` has prefix "client_token_".
- `token` doesn't have any prefix.


----


# All chat/responses/messages goes to same endpoint (`completions`)

## we expect `provider_request` to be used a google-format AI request
curl --request POST \
 --url https://cloud.zed.dev/completions \
 --header 'authorization: Bearer $API_KEY' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-client-supports-status-messages: true' \
 --header 'x-zed-client-supports-stream-ended-request-completion-status: true' \
 --header 'x-zed-version: 1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734' \
 --data @req-stream-gemini-3-5-flash.json > resp-stream-gemini-3-5-flash.json
Note for google-format:
- `` mapped to `generationConfig.thinkingConfig.thinkingLevel`


## we expect `provider_request` to be used a anthropic-format AI request
curl --request POST \
 --url https://cloud.zed.dev/completions \
 --header 'authorization: Bearer $API_KEY' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-client-supports-status-messages: true' \
 --header 'x-zed-client-supports-stream-ended-request-completion-status: true' \
 --header 'x-zed-version: 1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734' \
 --data @req-stream-haiku.json > resp-stream-haiku.json
Note for anthropic-format:
- `max_tokens` mapped to `provider_request.max_tokens`
- `temperature` mapped to `provider_request.temperature`
- `thinking` mapped to `provider_request.thinking.type`

## we expect `provider_request` to be used a openai-format AI request

### turn-1
curl --request POST \
 --url https://cloud.zed.dev/completions \
 --header 'authorization: Bearer $API_KEY' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-client-supports-status-messages: true' \
 --header 'x-zed-client-supports-stream-ended-request-completion-status: true' \
 --header 'x-zed-version: 1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734' \
 --data @req-stream-gpt-5-nano-1.json > resp-stream-gpt-5-nano-1.json

### turn-2
curl --request POST \
 --url https://cloud.zed.dev/completions \
 --header 'authorization: Bearer $API_KEY' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-client-supports-status-messages: true' \
 --header 'x-zed-client-supports-stream-ended-request-completion-status: true' \
 --header 'x-zed-version: 1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734' \
 --data @req-stream-gpt-5-nano-2.json > resp-stream-gpt-5-nano-2.json
Note for openai-format:
- `max_tokens` mapped to `provider_request.max_tokens`
- `temperature` mapped to `provider_request.temperature`
- `thinking` mapped to `provider_request.thinking.type`

Note for all format AI request:
- `thread_id` is like a `session-id`, every turn will use same id
- maybe `prompt_id` is a last user message id, it's not verified yet, prompt_id always change every turn and i don't know does the id refer to User-prompt or it can be refer to AI-message or Tool-result or request-id (a request-id is not same as session id).
- `prompt_cache_key` always has same value like `thread_id`
- looks like `prompt_cache_key` only available in openai-format AI request


----


# Session Title generator
curl --request POST \
 --url https://cloud.zed.dev/completions \
 --header 'authorization: Bearer $API_KEY' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-client-supports-status-messages: true' \
 --header 'x-zed-client-supports-stream-ended-request-completion-status: true' \
 --header 'x-zed-version: 1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734' \
 --data @req-title-generator.json
Note:
- in zed code editor, always append AI message/reasoning in turn-1. so zed make 1 request to LLM then LLM send a response then zed 2 request (for LLM and for session title generator), maybe zed hope AI can make title more accurate by sending their thought (AI + user message)
- looks like an AI request is wrapped to zed's request, there is `provider_request` which has same format like general AI request (like openai or gemini or anthropic).


----


# List Models
curl --request GET \
 --url https://cloud.zed.dev/models \
 --header 'authorization: Bearer $API_KEY' \
 --header 'user-agent: Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)' \
 --header 'x-zed-client-supports-x-ai: true' > zed-models.json




