package opencodezenfree

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGenerateOpencodeSessionIDFormat(t *testing.T) {
	re := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	for range 10 {
		sid := GenerateOpencodeSessionID()
		if !re.MatchString(sid) {
			t.Fatalf("GenerateOpencodeSessionID() = %q, want format ses_<12 hex><14 base62>", sid)
		}
	}
	// Counter must advance the time-derived prefix so consecutive IDs differ.
	if a, b := GenerateOpencodeSessionID(), GenerateOpencodeSessionID(); a == b {
		t.Fatalf("consecutive session IDs must differ, got %q twice", a)
	}
}

func TestGenerateTraceHeadersPaired(t *testing.T) {
	traceparentRe := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	for range 10 {
		traceparent, b3 := GenerateTraceHeaders()
		if !traceparentRe.MatchString(traceparent) {
			t.Fatalf("traceparent = %q, want 00-<32hex>-<16hex>-01", traceparent)
		}
		parts := strings.Split(traceparent, "-")
		wantB3Prefix := parts[1] + "-" + parts[2] + "-1-"
		if !strings.HasPrefix(b3, wantB3Prefix) {
			t.Fatalf("b3 = %q must share trace/span with traceparent %q (prefix %q)", b3, traceparent, wantB3Prefix)
		}
		rest := strings.TrimPrefix(b3, wantB3Prefix)
		if matched, _ := regexp.MatchString(`^[0-9a-f]{16}$`, rest); !matched {
			t.Fatalf("b3 parent span = %q, want 16 hex", rest)
		}
	}
}

func TestGenerateProjectID(t *testing.T) {
	got := GenerateProjectID("GitHub.com", "/user/repo.git")
	// sha1("git-remote:github.com/user/repo")
	if want := "36bb1454163e61f6afb07930b90d493a186e5f41"; got != want {
		t.Fatalf("GenerateProjectID = %q, want %q", got, want)
	}
	fallback := GenerateProjectID("", "")
	if matched, _ := regexp.MatchString(`^[0-9a-f]{40}$`, fallback); !matched {
		t.Fatalf("empty-input project ID = %q, want 40 hex", fallback)
	}
}

func TestBuildHeadersMatchesWorkingCapture(t *testing.T) {
	headers := BuildHeaders(nil, nil, false)

	if headers["authorization"] != DefaultAuth {
		t.Errorf("authorization = %q, want %q", headers["authorization"], DefaultAuth)
	}
	if headers["x-opencode-client"] != "cli" {
		t.Errorf("x-opencode-client = %q, want cli", headers["x-opencode-client"])
	}
	if headers["user-agent"] != DefaultUserAgent {
		t.Errorf("user-agent = %q, want %q", headers["user-agent"], DefaultUserAgent)
	}

	// Session trio must carry the same value.
	sid := headers["x-opencode-session"]
	if sid == "" {
		t.Fatal("x-opencode-session header must always be present")
	}
	if headers["x-session-affinity"] != sid || headers["x-session-id"] != sid {
		t.Errorf("session trio diverged: session=%q affinity=%q id=%q", sid, headers["x-session-affinity"], headers["x-session-id"])
	}
	if matched, _ := regexp.MatchString(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`, sid); !matched {
		t.Errorf("x-opencode-session = %q, want ses_<12hex><14base62>", sid)
	}

	if headers["x-opencode-project"] == "" {
		t.Error("x-opencode-project header must always be present")
	}
	if headers["traceparent"] == "" || headers["b3"] == "" {
		t.Error("traceparent and b3 headers must always be present")
	}

	// Removed headers must never be sent.
	for _, removed := range []string{
		"http-referer", "x-title", "x-stainless-arch", "x-stainless-async",
		"x-stainless-lang", "x-stainless-os", "x-stainless-package-version",
		"x-stainless-read-timeout", "x-stainless-retry-count",
		"x-stainless-runtime", "x-stainless-runtime-version",
	} {
		if _, ok := headers[removed]; ok {
			t.Errorf("header %q must not be sent", removed)
		}
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
		"authorization":      "Bearer custom",
	}
	headers := BuildHeaders(nil, config, false)

	if headers["X-Opencode-Session"] != "my-custom-session" {
		t.Fatalf("config-provided x-opencode-session = %q, want %q", headers["X-Opencode-Session"], "my-custom-session")
	}
	if headers["user-agent"] != "MyCustomAgent/1.0" {
		t.Fatalf("config-provided user-agent = %q, want %q", headers["user-agent"], "MyCustomAgent/1.0")
	}
	if headers["authorization"] != "Bearer custom" {
		t.Fatalf("config-provided authorization = %q, want Bearer custom", headers["authorization"])
	}
}

func TestBuildHeadersContextOverrides(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-opencode-session": {"ctx-session-123"},
		"x-opencode-project": {"ctx-project-abc"},
	})

	headers := BuildHeaders(ctx, map[string]string{"x-opencode-session": "config-session"}, false)
	if headers["x-opencode-session"] != "ctx-session-123" {
		t.Fatalf("context-provided x-opencode-session = %q, want %q (context must win)", headers["x-opencode-session"], "ctx-session-123")
	}
	if headers["x-opencode-project"] != "ctx-project-abc" {
		t.Fatalf("context-provided x-opencode-project = %q, want ctx-project-abc", headers["x-opencode-project"])
	}
}

func TestResolveSessionIDAcceptsAffinityAliases(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"X-Session-Id": {"alias-session"},
	})
	if sid := ResolveSessionID(ctx, nil); sid != "alias-session" {
		t.Fatalf("ResolveSessionID = %q, want alias-session", sid)
	}
	if pid := ResolveProjectID(nil, map[string]string{"X-Project": "proj-1"}); pid != "proj-1" {
		t.Fatalf("ResolveProjectID = %q, want proj-1", pid)
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

func TestResolveOpencodeHeadersSharesTracePair(t *testing.T) {
	resolved := ResolveOpencodeHeaders(nil, nil)
	parts := strings.Split(resolved.Traceparent, "-")
	if len(parts) != 4 {
		t.Fatalf("traceparent = %q, want 4 dash-separated parts", resolved.Traceparent)
	}
	if !strings.HasPrefix(resolved.B3, parts[1]+"-"+parts[2]+"-1-") {
		t.Fatalf("b3 = %q must share trace/span with traceparent %q", resolved.B3, resolved.Traceparent)
	}
}
