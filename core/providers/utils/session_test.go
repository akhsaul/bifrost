package utils

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestResolveSessionIDContextSessionID(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeySessionID, "sticky-session-1")

	sid := ResolveSessionID(ctx, nil)
	if sid == nil || *sid != "sticky-session-1" {
		t.Fatalf("ResolveSessionID = %v, want sticky-session-1", sid)
	}
}

func TestResolveSessionIDExplicitHeaderWinsOverContext(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeySessionID, "ctx-session")
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-custom-session": {"header-session"},
	})

	sid := ResolveSessionID(ctx, nil, "x-custom-session")
	if sid == nil || *sid != "header-session" {
		t.Fatalf("ResolveSessionID = %v, want header-session (explicit header wins)", sid)
	}
}

func TestResolveSessionIDProviderHeaderNames(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"X-Custom-Session": {"alias-session"},
	})
	sid := ResolveSessionID(ctx, nil, "x-custom-session")
	if sid == nil || *sid != "alias-session" {
		t.Fatalf("ResolveSessionID = %v, want alias-session", sid)
	}

	// Config extra headers are the fallback when ctx has neither session nor header.
	sid = ResolveSessionID(nil, map[string]string{"X-Custom-Session": "config-session"}, "x-custom-session")
	if sid == nil || *sid != "config-session" {
		t.Fatalf("ResolveSessionID = %v, want config-session", sid)
	}

	// Context extra headers beat config extra headers.
	ctx2 := schemas.NewBifrostContext(nil, time.Time{})
	ctx2.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-custom-session": {"ctx-wins"},
	})
	sid = ResolveSessionID(ctx2, map[string]string{"x-custom-session": "config-loses"}, "x-custom-session")
	if sid == nil || *sid != "ctx-wins" {
		t.Fatalf("ResolveSessionID = %v, want ctx-wins", sid)
	}
}

func TestResolveSessionIDNilWhenAbsent(t *testing.T) {
	if sid := ResolveSessionID(nil, nil); sid != nil {
		t.Fatalf("ResolveSessionID = %v, want nil", *sid)
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	if sid := ResolveSessionID(ctx, nil); sid != nil {
		t.Fatalf("ResolveSessionID = %v, want nil", *sid)
	}
	// Unrelated headers must not match.
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-other": {"something"},
	})
	if sid := ResolveSessionID(ctx, map[string]string{"x-another": "else"}, "x-custom-session"); sid != nil {
		t.Fatalf("ResolveSessionID = %v, want nil", *sid)
	}
}

func TestResolveSessionIDTrimsWhitespace(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeySessionID, "  padded  ")
	sid := ResolveSessionID(ctx, nil)
	if sid == nil || *sid != "padded" {
		t.Fatalf("ResolveSessionID = %v, want padded", sid)
	}
}

func TestResolveSessionIDBlankIsAbsent(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeySessionID, "   ")
	if sid := ResolveSessionID(ctx, nil, "x-custom"); sid != nil {
		t.Fatalf("ResolveSessionID = %v, want nil for blank session", *sid)
	}
}
