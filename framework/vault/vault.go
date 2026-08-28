package vault

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// VaultProvider abstracts secret retrieval and storage across backends.
type VaultProvider interface {
	GetSecret(ctx context.Context, project, config, name string) (string, error)
	ListSecrets(ctx context.Context, project, config string) (map[string]string, error)
	SetSecret(ctx context.Context, project, config, name, value string) error
	DeleteSecret(ctx context.Context, project, config, name string) error
	Ping(ctx context.Context) error
	Close() error
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
}

// VaultManager manages vault operations, caching, path routing, and hooks.
type VaultManager struct {
	provider VaultProvider
	config   *Config
	logger   schemas.Logger

	mu    sync.RWMutex
	cache map[string]cacheEntry
	ttl   time.Duration
}

var (
	activeManagerMu sync.RWMutex
	activeManager   *VaultManager
)

// GetActiveManager returns the currently registered VaultManager, if any.
func GetActiveManager() *VaultManager {
	activeManagerMu.RLock()
	defer activeManagerMu.RUnlock()
	return activeManager
}

// NewVaultManager creates a new VaultManager for the given provider and config.
func NewVaultManager(provider VaultProvider, cfg *Config, logger schemas.Logger) *VaultManager {
	if cfg == nil {
		cfg = &Config{Prefix: defaultPrefix}
	}

	return &VaultManager{
		provider: provider,
		config:   cfg,
		logger:   logger,
		cache:    make(map[string]cacheEntry),
		ttl:      1 * time.Hour,
	}
}

// InitVaultManager initializes the vault backend and registers global hooks on schemas.
func InitVaultManager(cfg *Config, logger schemas.Logger) (*VaultManager, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, ErrVaultDisabled
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var provider VaultProvider
	var err error

	switch cfg.Type {
	case VaultTypeDoppler:
		provider, err = NewDopplerProvider(cfg.Doppler)
		if err != nil {
			return nil, fmt.Errorf("vault: failed to initialize doppler provider: %w", err)
		}
	default:
		return nil, fmt.Errorf("vault: provider %q not supported in OSS framework", cfg.Type)
	}

	// Verify connectivity
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Ping(pingCtx); err != nil {
		if logger != nil {
			logger.Warn("vault: ping check failed during startup: %v", err)
		}
	}

	manager := NewVaultManager(provider, cfg, logger)

	// Register hooks on schemas
	schemas.VaultResolveHook = manager.ResolveString
	schemas.VaultPrefixHook = manager.GetPrefix

	if cfg.IsReadAndWrite() {
		schemas.VaultStoreHook = manager.StoreString
		schemas.VaultRemoveHook = manager.RemoveString
	} else {
		schemas.VaultStoreHook = nil
		schemas.VaultRemoveHook = nil
	}

	activeManagerMu.Lock()
	activeManager = manager
	activeManagerMu.Unlock()

	if logger != nil {
		logger.Info("vault store initialized (backend=%s, prefix=%s, access_mode=%s)", cfg.Type, cfg.GetPrefix(), cfg.GetAccessMode())
	}

	return manager, nil
}

// GetProvider returns the underlying VaultProvider.
func (m *VaultManager) GetProvider() VaultProvider {
	if m == nil {
		return nil
	}
	return m.provider
}

// GetConfig returns the vault configuration.
func (m *VaultManager) GetConfig() *Config {
	if m == nil {
		return nil
	}
	return m.config
}

// GetPrefix returns the configured prefix for the vault manager.
func (m *VaultManager) GetPrefix() string {
	if m == nil || m.config == nil {
		return defaultPrefix
	}
	return m.config.GetPrefix()
}

// FlushCache clears the in-memory secret cache.
func (m *VaultManager) FlushCache() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cache = make(map[string]cacheEntry)
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.Info("vault: secret cache flushed")
	}
}

// Resolve resolves a raw vault reference string (e.g. "vault.my-proj/prd/KEY#field").
func (m *VaultManager) Resolve(ctx context.Context, rawRef string) (string, error) {
	if m == nil || m.provider == nil {
		return "", ErrVaultDisabled
	}

	cleanRef := strings.TrimPrefix(rawRef, "vault.")
	if cleanRef == "" {
		return "", ErrInvalidPath
	}

	// Separate base path and fragment (if any)
	basePath := cleanRef
	fragment := ""
	if idx := strings.IndexByte(cleanRef, '#'); idx >= 0 {
		basePath = cleanRef[:idx]
		fragment = cleanRef[idx+1:]
	}

	// Check cache for base secret
	rawSecret, found := m.getFromCache(basePath)
	if !found {
		candidates := m.buildCandidateLookups(basePath)
		if len(candidates) == 0 {
			return "", fmt.Errorf("%w: %q", ErrInvalidPath, basePath)
		}

		var lastErr error
		resolvedVal := ""

		for _, cand := range candidates {
			val, err := m.provider.GetSecret(ctx, cand.project, cand.config, cand.name)
			if err == nil {
				resolvedVal = val
				lastErr = nil
				break
			}
			lastErr = err
		}

		if lastErr != nil {
			return "", lastErr
		}

		rawSecret = resolvedVal
		m.setCache(basePath, rawSecret)
	}

	// If no fragment requested, return full value
	if fragment == "" {
		return rawSecret, nil
	}

	// Extract JSON fragment
	return extractJSONFragment(rawSecret, fragment)
}

// ResolveString fulfills schemas.VaultResolveHook signature by mutating *value.
func (m *VaultManager) ResolveString(ctx context.Context, value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	if !strings.HasPrefix(*value, "vault.") {
		return nil
	}

	resolved, err := m.Resolve(ctx, *value)
	if err != nil {
		return err
	}
	*value = resolved
	return nil
}

// StoreString pushes plaintext value into the vault at path and rewrites *value to vault ref.
func (m *VaultManager) StoreString(ctx context.Context, path string, value *string) error {
	if m == nil || m.provider == nil {
		return ErrVaultDisabled
	}
	if !m.config.IsReadAndWrite() {
		return ErrReadOnlyMode
	}
	if value == nil || *value == "" {
		return nil
	}

	project, config, name := m.resolveStoreTarget(path)
	if err := m.provider.SetSecret(ctx, project, config, name, *value); err != nil {
		return err
	}

	// Update cache
	m.setCache(path, *value)

	*value = "vault." + path
	return nil
}

// RemoveString deletes the secret at path (best effort).
func (m *VaultManager) RemoveString(ctx context.Context, path string) error {
	if m == nil || m.provider == nil || !m.config.IsReadAndWrite() {
		return nil
	}

	// Fragment references point to shared secrets and are never deleted
	if strings.IndexByte(path, '#') >= 0 {
		return nil
	}

	project, config, name := m.resolveStoreTarget(path)
	_ = m.provider.DeleteSecret(ctx, project, config, name)

	m.invalidateCache(path)
	return nil
}

// Close releases resources held by the manager and provider.
func (m *VaultManager) Close() error {
	if m == nil || m.provider == nil {
		return nil
	}
	return m.provider.Close()
}

type secretLookupCandidate struct {
	project string
	config  string
	name    string
}

func (m *VaultManager) buildCandidateLookups(path string) []secretLookupCandidate {
	clean := strings.TrimPrefix(path, "/")
	prefix := m.GetPrefix()
	trimmedPrefix := strings.TrimPrefix(clean, prefix+"/")

	var candidates []secretLookupCandidate
	seen := make(map[string]bool)

	addCandidate := func(proj, cfg, name string) {
		key := proj + ":" + cfg + ":" + name
		if name != "" && !seen[key] {
			seen[key] = true
			candidates = append(candidates, secretLookupCandidate{
				project: proj,
				config:  cfg,
				name:    name,
			})
		}
	}

	// 1. If path has explicit project/config/name (e.g. my-proj/prd/KEY)
	segments := strings.Split(trimmedPrefix, "/")
	if len(segments) >= 3 {
		proj := segments[0]
		cfg := segments[1]
		name := strings.Join(segments[2:], "/")
		addCandidate(proj, cfg, name)
		addCandidate(proj, cfg, normalizeSecretName(name))
	}

	rawSegments := strings.Split(clean, "/")
	if len(rawSegments) >= 3 && clean != trimmedPrefix {
		proj := rawSegments[0]
		cfg := rawSegments[1]
		name := strings.Join(rawSegments[2:], "/")
		addCandidate(proj, cfg, name)
		addCandidate(proj, cfg, normalizeSecretName(name))
	}

	// 2. Default project / config candidates
	// a. Direct clean path as name
	addCandidate("", "", clean)
	// b. Normalized clean path (e.g. BIFROST_PROVIDERS_ANTHROPIC_KEY)
	addCandidate("", "", normalizeSecretName(clean))
	// c. Trimmed prefix normalized (e.g. PROVIDERS_ANTHROPIC_KEY)
	if trimmedPrefix != clean {
		addCandidate("", "", trimmedPrefix)
		addCandidate("", "", normalizeSecretName(trimmedPrefix))
	}
	// d. Last segment normalized
	if len(rawSegments) > 0 {
		lastSeg := rawSegments[len(rawSegments)-1]
		addCandidate("", "", lastSeg)
		addCandidate("", "", normalizeSecretName(lastSeg))
	}

	return candidates
}

func (m *VaultManager) resolveStoreTarget(path string) (project, config, name string) {
	clean := strings.TrimPrefix(path, "/")
	prefix := m.GetPrefix()

	// If the path starts with the configured vault prefix (e.g. "bifrost/..."),
	// it is an auto-managed secret (table/row/column). Store it in the default
	// project and config with the full normalized path as the secret name.
	if prefix != "" && (clean == prefix || strings.HasPrefix(clean, prefix+"/")) {
		return "", "", normalizeSecretName(clean)
	}

	trimmed := strings.TrimPrefix(clean, prefix+"/")
	segments := strings.Split(trimmed, "/")
	if len(segments) >= 3 {
		return segments[0], segments[1], normalizeSecretName(strings.Join(segments[2:], "_"))
	}

	// Default project/config, name is normalized path
	return "", "", normalizeSecretName(clean)
}

func normalizeSecretName(name string) string {
	// Replaces '/', '-', '.' with '_' and uppercases
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '-' || r == '.' {
			b.WriteByte('_')
		} else if r >= 'a' && r <= 'z' {
			b.WriteRune(r - 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func extractJSONFragment(jsonStr, key string) (string, error) {
	node, err := sonic.Get([]byte(jsonStr), key)
	if err != nil {
		return "", fmt.Errorf("vault: key %q not found in secret JSON: %w", key, err)
	}
	val, err := node.String()
	if err != nil {
		// If not a string literal, return raw node representation
		raw, rawErr := node.Raw()
		if rawErr == nil {
			return raw, nil
		}
		return "", fmt.Errorf("vault: failed to parse fragment %q value: %w", key, err)
	}
	return val, nil
}

func (m *VaultManager) getFromCache(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.value, true
}

func (m *VaultManager) setCache(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cache[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(m.ttl),
	}
}

func (m *VaultManager) invalidateCache(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.cache, key)
}
