package bifrost_test

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestCanProviderKeyValueBeEmptyOpencodeZenFree(t *testing.T) {
	if !bifrost.CanProviderKeyValueBeEmpty(schemas.OpencodeZenFree) {
		t.Fatalf("CanProviderKeyValueBeEmpty(opencode-zen-free) = false, want true (keyless provider)")
	}
}
