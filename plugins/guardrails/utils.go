package guardrails

// ptr returns a pointer to s. Shared by tests and runtime helpers.
func ptr(s string) *string { return &s }
