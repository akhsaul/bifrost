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

func TestGenerateOpencodeOrgIDFormat(t *testing.T) {
	re := regexp.MustCompile(`^org_01[0-9A-HJKMNP-TV-Z]{24}$`)
	for range 10 {
		orgID := GenerateOpencodeOrgID()
		if !re.MatchString(orgID) {
			t.Fatalf("GenerateOpencodeOrgID() = %q, want format org_01<24 Crockford base32>", orgID)
		}
	}
	if a, b := GenerateOpencodeOrgID(), GenerateOpencodeOrgID(); a == b {
		t.Fatalf("consecutive org IDs must differ, got %q twice", a)
	}
}

func TestExtractWorkingDirectory(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"working directory: /tmp/opencode-tls-test\n", "/tmp/opencode-tls-test"},
		{"Working directory: /mnt/data/Projects/bifrost\n", "/mnt/data/Projects/bifrost"},
		{"<env>Working directory: /tmp/my-project</env>", "/tmp/my-project"},
		{"<env>\nWorking directory: /tmp/nested\n</env>", "/tmp/nested"},
		{"You are helpful.\nworking directory: /home/user/app\nDo this...", "/home/user/app"},
		{"<env>Working directory: ?</env>", ""},
		{"no working directory here", ""},
	}
	for _, tc := range cases {
		got := ExtractWorkingDirectory(tc.input)
		if got != tc.want {
			t.Errorf("ExtractWorkingDirectory(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestResolveProjectIDGateway(t *testing.T) {
	// Fallback to sha1("global")
	fallback := ResolveProjectID(nil, nil)
	if want := SHA1String("global"); fallback != want {
		t.Errorf("ResolveProjectID fallback = %q, want %q", fallback, want)
	}

	// Working directory in system prompt -> sha1(/mnt/data/Projects/bifrost)
	prompt := "You are a helpful assistant.\nWorking directory: /mnt/data/Projects/bifrost\n"
	fromPrompt := ResolveProjectID(nil, nil, prompt)
	if want := SHA1String("/mnt/data/Projects/bifrost"); fromPrompt != want {
		t.Errorf("ResolveProjectID fromPrompt = %q, want %q", fromPrompt, want)
	}

	// Placeholder ? in system prompt must fall back to global sha1
	placeholderPrompt := "<env>Working directory: ?</env>"
	fromPlaceholder := ResolveProjectID(nil, nil, placeholderPrompt)
	if want := SHA1String("global"); fromPlaceholder != want {
		t.Errorf("ResolveProjectID fromPlaceholder = %q, want %q", fromPlaceholder, want)
	}

	// Client header takes top priority
	clientHeader := map[string]schemas.SecretVar{
		"x-opencode-project": *schemas.NewSecretVar("4594fe459c9129c1eb163e2ab9f8917f4b9cf758"),
	}
	fromHeader := ResolveProjectID(nil, clientHeader, prompt)
	if want := "4594fe459c9129c1eb163e2ab9f8917f4b9cf758"; fromHeader != want {
		t.Errorf("ResolveProjectID fromHeader = %q, want %q", fromHeader, want)
	}
}

func TestResolveOrgID(t *testing.T) {
	// Default dynamic generated
	re := regexp.MustCompile(`^org_01[0-9A-HJKMNP-TV-Z]{24}$`)
	def := ResolveOrgID(nil, nil)
	if !re.MatchString(def) {
		t.Errorf("default ResolveOrgID = %q, want format org_01...", def)
	}

	// Config override
	cfg := map[string]schemas.SecretVar{
		"x-opencode-org-id": *schemas.NewSecretVar("org_custom_123"),
	}
	if got := ResolveOrgID(nil, cfg); got != "org_custom_123" {
		t.Errorf("ResolveOrgID config override = %q, want org_custom_123", got)
	}

	// Context override
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-org-id": {"org_context_456"},
	})
	if got := ResolveOrgID(ctx, cfg); got != "org_context_456" {
		t.Errorf("ResolveOrgID context override = %q, want org_context_456", got)
	}
}

func TestBuildHeadersMatchesWorkingCapture(t *testing.T) {
	headers := BuildHeaders(nil, nil, false)

	if headers["authorization"].Val != DefaultAuth {
		t.Errorf("authorization = %q, want %q", headers["authorization"].Val, DefaultAuth)
	}
	if headers["x-opencode-client"].Val != "cli" {
		t.Errorf("x-opencode-client = %q, want cli", headers["x-opencode-client"].Val)
	}
	if headers["user-agent"].Val != DefaultUserAgent {
		t.Errorf("user-agent = %q, want %q", headers["user-agent"].Val, DefaultUserAgent)
	}

	// Session quartet must carry the same value.
	sid := headers["x-opencode-session"].Val
	if sid == "" {
		t.Fatal("x-opencode-session header must always be present")
	}
	for _, hdr := range []string{"x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		if headers[hdr].Val != sid {
			t.Errorf("session quartet diverged: session=%q %s=%q", sid, hdr, headers[hdr].Val)
		}
	}
	if matched, _ := regexp.MatchString(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`, sid); !matched {
		t.Errorf("x-opencode-session = %q, want ses_<12hex><14base62>", sid)
	}

	// Org ID must be present and valid ULID Crockford Base32
	orgID := headers["x-opencode-org-id"].Val
	if matched, _ := regexp.MatchString(`^org_01[0-9A-HJKMNP-TV-Z]{24}$`, orgID); !matched {
		t.Errorf("x-opencode-org-id = %q, want org_01<24 Crockford base32>", orgID)
	}

	// Project ID must be 40 hex char SHA-1
	pid := headers["x-opencode-project"].Val
	if matched, _ := regexp.MatchString(`^[0-9a-f]{40}$`, pid); !matched {
		t.Errorf("x-opencode-project = %q, want 40 hex char SHA-1", pid)
	}

	if headers["traceparent"].Val == "" || headers["b3"].Val == "" {
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
	config := map[string]schemas.SecretVar{
		"X-Opencode-Session": *schemas.NewSecretVar("my-custom-session"),
		"user-agent":         *schemas.NewSecretVar("MyCustomAgent/1.0"),
		"authorization":      *schemas.NewSecretVar("Bearer custom"),
	}
	headers := BuildHeaders(nil, config, false)

	if headers["X-Opencode-Session"].Val != "my-custom-session" {
		t.Fatalf("config-provided x-opencode-session = %q, want %q", headers["X-Opencode-Session"].Val, "my-custom-session")
	}
	if headers["user-agent"].Val != "MyCustomAgent/1.0" {
		t.Fatalf("config-provided user-agent = %q, want %q", headers["user-agent"].Val, "MyCustomAgent/1.0")
	}
	if headers["authorization"].Val != "Bearer custom" {
		t.Fatalf("config-provided authorization = %q, want Bearer custom", headers["authorization"].Val)
	}
}

func TestBuildHeadersContextOverrides(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-opencode-session": {"ctx-session-123"},
		"x-opencode-project": {"ctx-project-abc"},
	})

	headers := BuildHeaders(ctx, map[string]schemas.SecretVar{"x-opencode-session": *schemas.NewSecretVar("config-session")}, false)
	if headers["x-opencode-session"].Val != "ctx-session-123" {
		t.Fatalf("context-provided x-opencode-session = %q, want %q (context must win)", headers["x-opencode-session"].Val, "ctx-session-123")
	}
	if headers["x-opencode-project"].Val != "ctx-project-abc" {
		t.Fatalf("context-provided x-opencode-project = %q, want ctx-project-abc", headers["x-opencode-project"].Val)
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
	if pid := ResolveProjectID(nil, map[string]schemas.SecretVar{"X-Project": *schemas.NewSecretVar("proj-1")}); pid != "proj-1" {
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
