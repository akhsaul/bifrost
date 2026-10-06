

# All idea that will be implemented for bifrost
1. enhance "export settings", currently there is missing config_store, key allowed_requests only written to config.json when the provider is a custom, etc.
2. enhance feature "Prompt Repository" to add custom headers and custom body, so user doesn't have to depend only on model-parameter datasheet.
3. guardrails use typesafe/Jev (an AI model with endpoint /decisions) can be used to detect secret, AI can see which secret related to context. Need to do more test to know how accurate the model find a secret without giving more examples. As i know AI need more examples to make it more accurate, otherwise it will be false positive.


# idea that maybe will not implemented because too many conflict with other features
1. let user adding more parameters (like reasoning effort, max context, etc) for model in all provider which will be used when client fetch bifrost-endpoint.com/v1/models, so it doesn't have to wait until datasheet model-parameters from bifrost updated.
2. in "model catalogs" and in tab menu "models", there is an action to open details model but some metadata can't be edit, so let's fix it by adding ability to edit metadata like context window, max input token, max output token, model pricing, etc, let user adding more parameters (like reasoning effort, max context, etc) for model in all provider which will be used when client fetch bifrost-endpoint.com/v1/models, so it doesn't have to wait until datasheet model-parameters from bifrost updated.


# already implemented in a half-way
1. make plugin "extra-detection", has feature: counting estimation token, detect if last user message is contains "git"+"commit" or start with "question" then set complexity to simple (no need to use complexity-router). to count token estimation with library like tiktoken with formula: estimation=result-from-tiktoken + 10% of result-from-tiktoken then saved it to headers, then the routing rules can detect it from headers. this is cleanest solution which doesn't touch routing rules.


# already implemented and already tested with live test:
- google_search (native grounding search from gemini), supported provider: gemini/aistudio, antigravity.
- custom provider: antigravity, b.ai, bitdeer, byteplus, cline, inferx, longcat, modal, morphllm, neuralwatt, opencode-zen-free, tokenrouter, tokenharbor, vyceai.
- guardrails using regex, betterleaks.
- guardrails apply to tool_calls result, tool_calls turn from AI, user message.
- guardrails actions: redact.
- guardrails redaction strategy: replace.
- guardrails redaction mode: reversible.
- add new provider with prefix "-free" for free version: opencode-free. use case: some provider need special headers or format for body request, so it should not merge with original provider to prevent conflict.
- adaptive routing: EWMA, Group EWMA and priority order, also already registered on routing rules.
- doppler as a vault provider.
- logging body request (body content) only when error.
- model pricing and model parameters resolver can resolve provider name "$provider-free" into "$provider", so provider "opencode-zen-free" can reuse same datasheet from provider "opencode-zen".
- agent setup, user can configure their agent (opencode, codex, etc) to use specific model/provider/virtual key.
## looks like using fake model AI route
- seekai.cc
- inference.dahl.global
- api.hcnsec.cn
## has been shutdown
- freetokenfaucet.com
- gorouter.app
## web chat AI, so we can't send `tools:[]`
- aisure.uk

# already implemented and but NOT tested with live test (only unit test or static test):
- guardrails by AI judge
- guardrails apply to "complexity router" with "fallback: LLM", MCP tools.
- guardrails actions: block, detect only.
- guardrails redaction strategy: mask, hash.
- guardrails redaction mode: permanent.

