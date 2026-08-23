package circuitbreaker

import (
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Store defines the storage abstraction for circuit breaker trip statuses.
type Store interface {
	// Trip marks a key+model as tripped until resetTime.
	Trip(keyID string, provider schemas.ModelProvider, model string, resetTime time.Time, reason string, statusCode int)
	// IsTripped checks if a key+model is currently tripped at time `now`.
	IsTripped(keyID string, provider schemas.ModelProvider, model string, now time.Time) bool
	// GetStatus returns the breaker status for a key+model, if tripped and active.
	GetStatus(keyID string, provider schemas.ModelProvider, model string, now time.Time) (*BreakerStatus, bool)
	// Reset manually clears a tripped key+model.
	Reset(keyID string, provider schemas.ModelProvider, model string)
	// ResetAll clears all tripped states.
	ResetAll()
	// GetAllTripped returns all currently tripped entries.
	GetAllTripped(now time.Time) []BreakerStatus
}

// MemoryStore implements an in-memory Store with thread-safe access and automatic cleanup.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]BreakerStatus
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[string]BreakerStatus),
	}
}

func makeStoreKey(keyID string, provider schemas.ModelProvider, model string) string {
	return keyID + "::" + string(provider) + "::" + model
}

func (m *MemoryStore) Trip(keyID string, provider schemas.ModelProvider, model string, resetTime time.Time, reason string, statusCode int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	k := makeStoreKey(keyID, provider, model)
	m.entries[k] = BreakerStatus{
		KeyID:      keyID,
		Model:      model,
		Provider:   provider,
		TrippedAt:  time.Now(),
		ResetTime:  resetTime,
		Reason:     reason,
		StatusCode: statusCode,
	}
}

func (m *MemoryStore) IsTripped(keyID string, provider schemas.ModelProvider, model string, now time.Time) bool {
	m.mu.RLock()
	k := makeStoreKey(keyID, provider, model)
	status, exists := m.entries[k]
	m.mu.RUnlock()

	if !exists {
		return false
	}

	if status.IsActive(now) {
		return true
	}

	// Clean up expired entry in write lock
	m.mu.Lock()
	if cur, ok := m.entries[k]; ok && !cur.IsActive(now) {
		delete(m.entries, k)
	}
	m.mu.Unlock()

	return false
}

func (m *MemoryStore) GetStatus(keyID string, provider schemas.ModelProvider, model string, now time.Time) (*BreakerStatus, bool) {
	m.mu.RLock()
	k := makeStoreKey(keyID, provider, model)
	status, exists := m.entries[k]
	m.mu.RUnlock()

	if !exists {
		return nil, false
	}

	if status.IsActive(now) {
		return &status, true
	}

	// Clean up expired entry
	m.mu.Lock()
	if cur, ok := m.entries[k]; ok && !cur.IsActive(now) {
		delete(m.entries, k)
	}
	m.mu.Unlock()

	return nil, false
}

func (m *MemoryStore) Reset(keyID string, provider schemas.ModelProvider, model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, makeStoreKey(keyID, provider, model))
}

func (m *MemoryStore) ResetAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]BreakerStatus)
}

func (m *MemoryStore) GetAllTripped(now time.Time) []BreakerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tripped := make([]BreakerStatus, 0, len(m.entries))
	for _, status := range m.entries {
		if status.IsActive(now) {
			tripped = append(tripped, status)
		}
	}
	return tripped
}
