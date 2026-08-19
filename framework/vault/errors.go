package vault

import "errors"

var (
	// ErrVaultDisabled is returned when attempting vault operations on a disabled vault.
	ErrVaultDisabled = errors.New("vault: integration is not enabled")

	// ErrSecretNotFound is returned when the requested secret cannot be found.
	ErrSecretNotFound = errors.New("vault: secret not found")

	// ErrInvalidPath is returned when a vault reference path is malformed.
	ErrInvalidPath = errors.New("vault: invalid secret path")

	// ErrMissingConfig is returned when required vault configuration is omitted.
	ErrMissingConfig = errors.New("vault: missing configuration")

	// ErrMissingToken is returned when the Doppler API token is not provided.
	ErrMissingToken = errors.New("vault: doppler token is required")

	// ErrMissingProjectOrConfig is returned when project or config is required but missing.
	ErrMissingProjectOrConfig = errors.New("vault: doppler project and config are required")

	// ErrReadOnlyMode is returned when attempting a write or delete in read-only access mode.
	ErrReadOnlyMode = errors.New("vault: store is configured in read_only mode")

	// ErrUnauthorized is returned when the Doppler API rejects the authentication token.
	ErrUnauthorized = errors.New("vault: doppler unauthorized, check token permissions")

	// ErrRateLimited is returned when the Doppler API rate limit is exceeded.
	ErrRateLimited = errors.New("vault: doppler api rate limited")
)
