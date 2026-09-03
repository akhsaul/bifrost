package vault

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestConfigValidateAccessMode(t *testing.T) {
	base := func(mode AccessMode) *Config {
		return &Config{
			Enabled:    true,
			Type:       VaultTypeDoppler,
			AccessMode: mode,
			Doppler: &DopplerConfig{
				Token: schemas.NewSecretVar("dp.st.test"),
			},
		}
	}

	// Empty is allowed: it defaults to read_only (see GetAccessMode).
	assert.NoError(t, base("").Validate())
	assert.NoError(t, base(AccessModeReadOnly).Validate())
	assert.NoError(t, base(AccessModeReadAndWrite).Validate())

	// Everything else must fail validation ("read" is the real-world typo seen
	// in production).
	assert.ErrorIs(t, base("read").Validate(), ErrInvalidAccessMode)
	assert.ErrorIs(t, base("READ_ONLY").Validate(), ErrInvalidAccessMode)
	assert.ErrorIs(t, base("write").Validate(), ErrInvalidAccessMode)
}
