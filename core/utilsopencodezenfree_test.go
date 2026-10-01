package bifrost_test

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestCanProviderKeyValueBeEmptyOpencodeFree(t *testing.T) {
	if !bifrost.CanProviderKeyValueBeEmpty(schemas.OpencodeFree) {
		t.Fatalf("CanProviderKeyValueBeEmpty(opencode-free) = false, want true (keyless provider)")
	}
}
