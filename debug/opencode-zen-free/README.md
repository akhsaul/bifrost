# Known Issues
- currently no issues opened here.

# Known Limitation
- since opencode zen never translate format request to another supported format for AI then bifrost need parameter "supported_endpoints" in datasheet model-parameters to detect what format should be used.
- opencode zen free tier require to have tools with names (edit, read, shell), otherwise the server will refuse.
- opencode zen free tier require to have headers "x-opencode-client", "x-opencode-project", "x-opencode-session", "x-session-affinity", "x-session-id", "traceparent", "b3", "user-agent". read more how to generate id, project id, b3, traceparent at file opencode-generate-headers.md .
- opencode zen free tier require a system prompt to be non-empty string, default to "You are helpfull assistant".
- currently bifrost prioritize model name as detection method to determine which endpoint would be used, if it's "gemini" then use gemini format, otherwise bifrost trying to guess by hit /v1/chat/completions then fallback to /v1/responses then fallback to /v1/messages. MAYBE WE HAVE TO STICK ON datasheet model-parameters rather trying to guessing to REDUCE DELAY, BUT HOW TO MAKE USER KNOW WHEN USER TRY TO USE NEW MODEL WHICH NOT ALREADY ADDED IN datasheet?.
- sometime client/agent sent 1 prompt to generate title for session, opencode zen free tier always refuse if it's have any function tools.


# Already fixed
- some models only support specific endpoint like /v1/responses or /v1/chat/completions, opencode zen server never translate request with format for /v1/chat/completions into format /v1/responses.
- always carry function tools with names (edit, read, shell) to make prevent opencode server returning error/failed, also overwritten by client/agent when they are already define same function names.
- always carry required headers, also overwritten by client/agent when they sent same headers.
- always carry default system prompt "You are helpfull assistant", also overwritten by client/agent when they sent system prompt (detect from message.role=system or instructions or etc, AI format request aware).
- always remove function tools when system prompt contains string "title generator", "conversation title", "generate title". Antigravity CLI use string "title generator", Opencode CLI use string "title generator", other client/agent is unknown.

