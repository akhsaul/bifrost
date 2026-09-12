package opencodefree

import (
	"regexp"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGenerateSessionIDFormat(t *testing.T) {
	re := regexp.MustCompile(`^\d{8}_\d{6}_[a-f0-9]{6}$`)
	for range 10 {
		sid := GenerateSessionID()
		if !re.MatchString(sid) {
			t.Fatalf("GenerateSessionID() = %q, want format YYYYMMDD_HHMMSS_<6 hex chars>", sid)
		}
	}
}

func TestBuildHeadersNonStreaming(t *testing.T) {
	headers := BuildHeaders(nil, nil, false)

	expected := map[string]string{
		"http-referer":                "https://hermes-agent.nousresearch.com",
		"user-agent":                  "HermesAgent/0.21.2",
		"x-stainless-arch":            "x64",
		"x-stainless-async":           "false",
		"x-stainless-lang":            "python",
		"x-stainless-os":              "Linux",
		"x-stainless-package-version": "2.24.0",
		"x-stainless-read-timeout":    "1800.0",
		"x-stainless-retry-count":     "0",
		"x-stainless-runtime":         "CPython",
		"x-stainless-runtime-version": "3.11.16",
		"x-title":                     "Hermes Agent",
	}

	for k, v := range expected {
		if got, ok := headers[k]; !ok || got != v {
			t.Errorf("header %q = %q (present=%v), want %q", k, got, ok, v)
		}
	}
	if _, ok := headers["x-opencode-session"]; !ok {
		t.Error("x-opencode-session header must always be present")
	}
}

func TestBuildHeadersStreamingAccept(t *testing.T) {
	// Accept header is intentionally NOT sent by BuildHeaders (managed by the
	// openai streaming helpers); this just guards the no-crash / basic contract.
	headers := BuildHeaders(nil, nil, true)
	if _, ok := headers["x-opencode-session"]; !ok {
		t.Fatal("x-opencode-session header must always be present")
	}
}

func TestBuildHeadersConfigOverrides(t *testing.T) {
	config := map[string]string{
		"X-Opencode-Session": "my-custom-session",
		"user-agent":         "MyCustomAgent/1.0",
	}
	headers := BuildHeaders(nil, config, false)

	if headers["X-Opencode-Session"] != "my-custom-session" {
		t.Fatalf("config-provided x-opencode-session = %q, want %q", headers["X-Opencode-Session"], "my-custom-session")
	}
	if headers["user-agent"] != "MyCustomAgent/1.0" {
		t.Fatalf("config-provided user-agent = %q, want %q", headers["user-agent"], "MyCustomAgent/1.0")
	}
	// Other defaults must remain
	if headers["x-title"] != "Hermes Agent" {
		t.Fatalf("x-title = %q, want %q", headers["x-title"], "Hermes Agent")
	}
}

func TestBuildHeadersIncludesEmptyAuthorization(t *testing.T) {
	headers := BuildHeaders(nil, nil, false)
	val, ok := headers["authorization"]
	if !ok {
		t.Fatal("expected 'authorization' header to be present")
	}
	if val != "" {
		t.Fatalf("expected empty authorization header, got %q", val)
	}
}

func TestBuildHeadersContextOverrides(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-opencode-session": {"ctx-session-123"},
	})

	headers := BuildHeaders(ctx, map[string]string{"x-opencode-session": "config-session"}, false)
	if headers["x-opencode-session"] != "ctx-session-123" {
		t.Fatalf("context-provided x-opencode-session = %q, want %q (context must win)", headers["x-opencode-session"], "ctx-session-123")
	}
}

func TestResolveSessionIDContextSessionID(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeySessionID, "sticky-session-1")

	sid := ResolveSessionID(ctx, nil)
	if sid != "sticky-session-1" {
		t.Fatalf("ResolveSessionID = %q, want %q", sid, "sticky-session-1")
	}
}
