package schemas_test

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBaiProviderRegistered(t *testing.T) {
	if schemas.Bai != "bai" {
		t.Fatalf("Bai = %q, want %q", schemas.Bai, "bai")
	}
	if !slices.Contains(schemas.StandardProviders, schemas.Bai) {
		t.Fatalf("StandardProviders must contain %q", schemas.Bai)
	}
}
