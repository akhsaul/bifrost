package guardrails

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// hashRedaction hashes a secret and returns a truncated hex placeholder.
func hashRedaction(val string) string {
	sum := sha256.Sum256([]byte(val))
	return fmt.Sprintf("[SECRET:%s]", hex.EncodeToString(sum[:])[:12])
}
