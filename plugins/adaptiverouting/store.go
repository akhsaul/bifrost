package adaptiverouting

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// MetricSample represents a single request execution outcome.
type MetricSample struct {
	Timestamp  time.Time
	Duration   time.Duration
	TTFT       time.Duration
	StatusCode int
	IsError    bool
}

// TargetMetricsBuffer is a lock-protected ring buffer collecting recent samples for a single TargetID.
type TargetMetricsBuffer struct {
	mu      sync.RWMutex
	samples []MetricSample
	maxSize int
	head    int
	count   int

	// Running metrics
	ewmaLatencyMs float64
	ewmaTTFTMs    float64
	hasEWMA       bool
}

func newTargetMetricsBuffer(maxSize int) *TargetMetricsBuffer {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &TargetMetricsBuffer{
		samples: make([]MetricSample, maxSize),
		maxSize: maxSize,
	}
}

func (b *TargetMetricsBuffer) record(sample MetricSample, alpha float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.samples[b.head] = sample
	b.head = (b.head + 1) % b.maxSize
	if b.count < b.maxSize {
		b.count++
	}

	durationMs := float64(sample.Duration.Microseconds()) / 1000.0
	ttftMs := float64(sample.TTFT.Microseconds()) / 1000.0

	// Effective sample latency: blend TTFT if available
	sampleLatencyMs := durationMs
	if ttftMs > 0 {
		sampleLatencyMs = 0.7*ttftMs + 0.3*durationMs
	}

	if !b.hasEWMA {
		b.ewmaLatencyMs = sampleLatencyMs
		b.ewmaTTFTMs = ttftMs
		b.hasEWMA = true
	} else {
		b.ewmaLatencyMs = alpha*sampleLatencyMs + (1.0-alpha)*b.ewmaLatencyMs
		if ttftMs > 0 {
			if b.ewmaTTFTMs == 0 {
				b.ewmaTTFTMs = ttftMs
			} else {
				b.ewmaTTFTMs = alpha*ttftMs + (1.0-alpha)*b.ewmaTTFTMs
			}
		}
	}
}

func (b *TargetMetricsBuffer) getStats(target TargetID, window time.Duration, now time.Time) TargetStats {
	b.mu.RLock()
	defer b.mu.RUnlock()

	cutoff := now.Add(-window)
	var durations []float64
	var successCount, rateLimit429Count, error5xxCount, total int64
	var lastObserved time.Time

	for i := 0; i < b.count; i++ {
		idx := (b.head - 1 - i + b.maxSize) % b.maxSize
		s := b.samples[idx]
		if s.Timestamp.Before(cutoff) {
			break
		}
		total++
		if s.Timestamp.After(lastObserved) {
			lastObserved = s.Timestamp
		}

		durationMs := float64(s.Duration.Microseconds()) / 1000.0
		durations = append(durations, durationMs)

		if s.StatusCode == 429 {
			rateLimit429Count++
		} else if s.StatusCode >= 500 || s.IsError {
			error5xxCount++
		} else {
			successCount++
		}
	}

	p90 := 0.0
	if len(durations) > 0 {
		sort.Float64s(durations)
		p90Idx := int(math.Ceil(float64(len(durations))*0.90)) - 1
		if p90Idx < 0 {
			p90Idx = 0
		}
		if p90Idx < len(durations) {
			p90 = durations[p90Idx]
		}
	}

	return TargetStats{
		TargetID:          target,
		EWMALatencyMs:     b.ewmaLatencyMs,
		TTFTMs:            b.ewmaTTFTMs,
		P90LatencyMs:      p90,
		SuccessCount:      successCount,
		RateLimit429Count: rateLimit429Count,
		Error5xxCount:     error5xxCount,
		TotalRequests:     total,
		LastObservedAt:    lastObserved,
	}
}

// Store defines the storage abstraction for collecting and querying performance metrics.
type Store interface {
	RecordMetric(ctx context.Context, target TargetID, duration time.Duration, ttft time.Duration, statusCode int, isError bool)
	GetStats(ctx context.Context, target TargetID, window time.Duration) TargetStats
	GetAllStats(ctx context.Context, window time.Duration) map[TargetID]TargetStats
	ResetAll()
}

// MemoryStore provides a high-performance in-memory Store with lock-free reading where possible.
type MemoryStore struct {
	mu      sync.RWMutex
	buffers map[string]*TargetMetricsBuffer
	alpha   float64
}

// NewMemoryStore creates a new in-memory metrics store.
func NewMemoryStore(alpha float64) *MemoryStore {
	if alpha <= 0 || alpha > 1.0 {
		alpha = 0.2
	}
	return &MemoryStore{
		buffers: make(map[string]*TargetMetricsBuffer),
		alpha:   alpha,
	}
}

func (m *MemoryStore) getOrCreateBuffer(target TargetID) *TargetMetricsBuffer {
	key := target.String()
	m.mu.RLock()
	buf, ok := m.buffers[key]
	m.mu.RUnlock()
	if ok {
		return buf
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if buf, ok = m.buffers[key]; ok {
		return buf
	}
	buf = newTargetMetricsBuffer(500)
	m.buffers[key] = buf
	return buf
}

func (m *MemoryStore) RecordMetric(_ context.Context, target TargetID, duration time.Duration, ttft time.Duration, statusCode int, isError bool) {
	if target.Provider == "" {
		return
	}
	buf := m.getOrCreateBuffer(target)
	buf.record(MetricSample{
		Timestamp:  time.Now(),
		Duration:   duration,
		TTFT:       ttft,
		StatusCode: statusCode,
		IsError:    isError,
	}, m.alpha)
}

func (m *MemoryStore) GetStats(_ context.Context, target TargetID, window time.Duration) TargetStats {
	key := target.String()
	m.mu.RLock()
	buf, ok := m.buffers[key]
	m.mu.RUnlock()
	if !ok {
		return TargetStats{TargetID: target}
	}
	return buf.getStats(target, window, time.Now())
}

func (m *MemoryStore) GetAllStats(_ context.Context, window time.Duration) map[TargetID]TargetStats {
	m.mu.RLock()
	keys := make([]string, 0, len(m.buffers))
	for k := range m.buffers {
		keys = append(keys, k)
	}
	m.mu.RUnlock()

	now := time.Now()
	res := make(map[TargetID]TargetStats, len(keys))
	for _, k := range keys {
		m.mu.RLock()
		buf, ok := m.buffers[k]
		m.mu.RUnlock()
		if ok {
			// Parse provider and model from key
			target := parseTargetIDString(k)
			res[target] = buf.getStats(target, window, now)
		}
	}
	return res
}

func (m *MemoryStore) ResetAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buffers = make(map[string]*TargetMetricsBuffer)
}

func parseTargetIDString(s string) TargetID {
	var target TargetID
	// Formats: "provider/model#key_id" or "provider/model"
	var prov, model, key string
	var slashIdx = -1
	var hashIdx = -1

	for i := 0; i < len(s); i++ {
		if s[i] == '/' && slashIdx == -1 {
			slashIdx = i
		} else if s[i] == '#' && hashIdx == -1 {
			hashIdx = i
		}
	}

	if slashIdx != -1 {
		prov = s[:slashIdx]
		if hashIdx != -1 {
			model = s[slashIdx+1 : hashIdx]
			key = s[hashIdx+1:]
		} else {
			model = s[slashIdx+1:]
		}
	} else {
		prov = s
	}

	target.Provider = schemas.ModelProvider(prov)
	target.Model = model
	target.KeyID = key
	return target
}
