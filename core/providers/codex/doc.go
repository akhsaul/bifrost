// Package codex implements the Codex provider (OpenAI Codex OAuth at
// https://chatgpt.com/backend-api/codex).
//
// Inference speaks the OpenAI Responses API natively over
// chatgpt.com/backend-api/codex: unary via POST /responses, streaming via the
// same endpoint with stream:true, and ListModels via GET /models. Chat
// completions are served by converting through Responses (requests via
// ToResponsesRequest, responses via ToBifrostChatResponse), which is the same
// shape cline and groq use for providers without a native chat endpoint.
//
// Auth is dual-mode, checked in this order:
//
//  1. A static access token in Key.Value, sent verbatim as "Bearer <value>".
//  2. CodexKeyConfig holding a PKCE-flow refresh token. Bifrost exchanges it
//     at https://auth.openai.com/oauth/token for a short-lived access token
//     (rotating: every refresh returns a new refresh token, which is adopted
//     for the next mint).
//
// The workspace (chatgpt-account-id header) binds per request: an explicit
// AccountID in CodexKeyConfig wins, otherwise it is discovered once from
// GET /wham/accounts/check and cached with the token. ListModels filters the
// catalog by the resolved account's plan_type against each model's
// available_in_plans, so only models the plan can serve are returned.
package codex
