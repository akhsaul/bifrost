package vault

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// VaultType represents the backend secret manager.
type VaultType string

const (
	VaultTypeDoppler   VaultType = "doppler"
	VaultTypeAWS       VaultType = "aws-secrets-manager"
	VaultTypeGCP       VaultType = "gcp-secret-manager"
	VaultTypeHashiCorp VaultType = "hashicorp-vault"
)

// AccessMode specifies whether Bifrost only resolves references or also auto-stores secrets.
type AccessMode string

const (
	AccessModeReadOnly     AccessMode = "read_only"
	AccessModeReadAndWrite AccessMode = "read_and_write"
)

const (
	defaultPrefix     = "bifrost"
	defaultDopplerURL = "https://api.doppler.com"
	defaultTimeoutSec = 10
)

// DopplerConfig holds settings for Doppler SecretOps integration.
type DopplerConfig struct {
	// Token is the Doppler API token (Service Token dp.st.*, Service Account Token dp.sa.*, Personal Token dp.pt.*, CLI token).
	Token *schemas.SecretVar `json:"token"`

	// Project is the default Doppler project name or slug.
	Project *schemas.SecretVar `json:"project,omitempty"`

	// Config is the default Doppler config / environment name (e.g. dev, stg, prd).
	Config *schemas.SecretVar `json:"config,omitempty"`

	// BaseURL is the Doppler API endpoint. Defaults to https://api.doppler.com if unset.
	BaseURL *schemas.SecretVar `json:"base_url,omitempty"`

	// TimeoutSec is the HTTP request timeout in seconds. Defaults to 10.
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// GetBaseURL returns the configured base URL or the Doppler default.
func (d *DopplerConfig) GetBaseURL() string {
	if d != nil && d.BaseURL != nil && d.BaseURL.GetValue() != "" {
		return d.BaseURL.GetValue()
	}
	return defaultDopplerURL
}

// GetTimeoutSec returns the configured timeout in seconds, defaulting to 10s.
func (d *DopplerConfig) GetTimeoutSec() int {
	if d != nil && d.TimeoutSec > 0 {
		return d.TimeoutSec
	}
	return defaultTimeoutSec
}

// Config represents the top-level vault_store configuration in config.json.
type Config struct {
	Enabled    bool           `json:"enabled"`
	Type       VaultType      `json:"type"`
	Prefix     string         `json:"prefix,omitempty"`
	AccessMode AccessMode     `json:"access_mode,omitempty"`
	Doppler    *DopplerConfig `json:"doppler,omitempty"`

	// AWS, GCP, HashiCorp generic config payload preservation for enterprise schema compatibility
	AWS       json.RawMessage `json:"aws,omitempty"`
	GCP       json.RawMessage `json:"gcp,omitempty"`
	HashiCorp json.RawMessage `json:"hashicorp,omitempty"`
}

// GetPrefix returns the configured vault path prefix, defaulting to "bifrost".
func (c *Config) GetPrefix() string {
	if c == nil || c.Prefix == "" {
		return defaultPrefix
	}
	return c.Prefix
}

// GetAccessMode returns the configured access mode, defaulting to read_only.
func (c *Config) GetAccessMode() AccessMode {
	if c == nil || c.AccessMode == "" {
		return AccessModeReadOnly
	}
	return c.AccessMode
}

// IsReadAndWrite reports whether vault store allows mutations.
func (c *Config) IsReadAndWrite() bool {
	return c != nil && c.GetAccessMode() == AccessModeReadAndWrite
}

// Validate checks the configuration for semantic correctness.
func (c *Config) Validate() error {
	if c == nil || !c.Enabled {
		return nil
	}

	switch c.Type {
	case VaultTypeDoppler:
		if c.Doppler == nil {
			return fmt.Errorf("%w: 'doppler' block is required when type is doppler", ErrMissingConfig)
		}
		if c.Doppler.Token == nil || c.Doppler.Token.GetValue() == "" {
			return ErrMissingToken
		}
	case VaultTypeAWS, VaultTypeGCP, VaultTypeHashiCorp:
		// Preserved for enterprise backends
		return nil
	default:
		return fmt.Errorf("vault: unsupported vault type %q", c.Type)
	}

	return nil
}
