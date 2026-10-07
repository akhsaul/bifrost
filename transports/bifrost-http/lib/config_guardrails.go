package lib

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/guardrails"
)

func loadGuardrailsConfig(ctx context.Context, config *Config, configData *ConfigData) {
	if config.ConfigStore != nil {
		row, err := config.ConfigStore.GetConfig(ctx, tables.ConfigGuardrailsConfigKey)
		if err != nil {
			if !errors.Is(err, configstore.ErrNotFound) {
				logger.Warn("failed to load guardrails config from db: %v", err)
			}
		} else if row != nil && row.Value != "" {
			var parsed guardrails.Config
			if err := json.Unmarshal([]byte(row.Value), &parsed); err != nil {
				logger.Warn("failed to unmarshal guardrails config from db: %v", err)
			} else {
				config.GuardrailsConfig = &parsed
				return
			}
		}
	}
	
	// Fallback to file config if db load fails/not found, or if file is source of truth?
	// Wait, for guardrails, do we support file config? We did map it in ConfigData.
	// So we can do that.
	if configData.GuardrailsConfig != nil {
		config.GuardrailsConfig = configData.GuardrailsConfig
	}
}
