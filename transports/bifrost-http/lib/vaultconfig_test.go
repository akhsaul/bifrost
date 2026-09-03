package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vault"
	"github.com/stretchr/testify/require"
)

func TestInitVaultRejectsInvalidAccessMode(t *testing.T) {
	configData := &ConfigData{
		ConfigStoreConfig: &configstore.Config{
			Enabled: true,
			VaultStore: &vault.Config{
				Enabled:    true,
				Type:       vault.VaultTypeDoppler,
				AccessMode: "read",
				Doppler:    &vault.DopplerConfig{Token: schemas.NewSecretVar("dp.st.test")},
			},
		},
	}

	err := initVault(configData)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid access_mode")
}

func TestInitVaultNoopWhenAbsentOrDisabled(t *testing.T) {
	require.NoError(t, initVault(nil))
	require.NoError(t, initVault(&ConfigData{}))
	require.NoError(t, initVault(&ConfigData{
		ConfigStoreConfig: &configstore.Config{Enabled: true},
	}))
}
