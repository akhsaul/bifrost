package complexity

import (
	"context"
	"errors"
	"time"
)

// warmupRateLimitRetryDelay is how long exemplar warmup waits after a
// rate-limited provider call before retrying the same call. Per-minute quotas
// reset on a minute boundary, so retrying sooner just burns another 429;
// waiting out the window is what lets a key with a lower limit than the pacing
// rate still finish. A variable (not a constant) so tests can shrink it.
var warmupRateLimitRetryDelay = time.Minute

// warmupRateLimitMaxRetries bounds consecutive rate-limited retries of one
// warmup provider call. Past it the warmup fails with the rate-limited reason
// (and the retry endpoint can restart it) rather than stalling the warmup
// worker against a permanently exhausted quota.
const warmupRateLimitMaxRetries = 5

// warmupPacer spaces exemplar-warmup provider calls to at most a configured
// requests-per-minute rate. Saving a config embeds every reference phrase in
// batches, and without pacing that burst trips per-minute key quotas —
// failing the whole warmup on the first 429. Warmup runs on one serial worker
// and every provider call in it goes through this pacer, so the rate holds
// across the dimension probe and all batches. Nil means no pacing, which only
// unnormalized configs (tests) can produce: normalization always applies the
// default.
type warmupPacer struct {
	interval time.Duration
	last     time.Time
}

// newWarmupPacer builds a pacer for requestsPerMinute, or nil when pacing is
// disabled. Non-positive means disabled rather than defaulted: the caller
// (warmSemanticExemplars) receives normalized configs in production, where the
// default is already applied, and only tests pass zero.
func newWarmupPacer(requestsPerMinute int) *warmupPacer {
	if requestsPerMinute <= 0 {
		return nil
	}
	return &warmupPacer{interval: time.Minute / time.Duration(requestsPerMinute)}
}

// wait blocks until the next provider call is due. The first call fires
// immediately; every later one waits for a full interval since the previous
// call. A cancelled context aborts the wait so reconfiguration and shutdown
// never stall behind the pacer.
func (w *warmupPacer) wait(ctx context.Context) error {
	if w == nil || w.interval <= 0 {
		return nil
	}
	now := time.Now()
	if !w.last.IsZero() {
		if deadline := w.last.Add(w.interval); now.Before(deadline) {
			if err := sleepContext(ctx, time.Until(deadline)); err != nil {
				return err
			}
			now = time.Now()
		}
	}
	w.last = now
	return nil
}

// sleepContext sleeps for d or until ctx is done, reporting cancellation as an
// error so paced and backed-off waits stay interruptible.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isWarmupRateLimited reports whether err is a provider rate-limit refusal.
// Only that failure is worth waiting out: auth, model, timeout, and response
// failures fail the warmup immediately, exactly as before.
func isWarmupRateLimited(err error) bool {
	var failure semanticWarmupFailure
	return errors.As(err, &failure) && failure.SemanticFailureReason() == SemanticFailureRateLimited
}

// embedWarmupPhrase embeds one phrase for exemplar warmup: paced, and retried
// after a one-minute wait when the provider reports rate limiting. Any other
// failure — including batch-unsupported, which the caller handles by falling
// back to single-input embeds — returns immediately.
func embedWarmupPhrase(ctx context.Context, pacer *warmupPacer, embed EmbeddingFunc, semantic *SemanticConfig, phrase string) ([]float32, error) {
	for attempt := 0; ; attempt++ {
		if err := pacer.wait(ctx); err != nil {
			return nil, err
		}
		embedding, err := embed(ctx, semantic, phrase)
		if err == nil {
			return embedding, nil
		}
		if !isWarmupRateLimited(err) || attempt >= warmupRateLimitMaxRetries {
			return nil, err
		}
		if err := sleepContext(ctx, warmupRateLimitRetryDelay); err != nil {
			return nil, err
		}
	}
}

// embedWarmupBatch embeds one bounded warmup batch: paced, and retried after
// a one-minute wait when the provider reports rate limiting. A provider that
// answers with the wrong vector count or indices still reports
// ErrBatchEmbeddingsUnsupported immediately so the caller can fall back to
// single-input embeds.
func embedWarmupBatch(ctx context.Context, pacer *warmupPacer, embedBatch BatchEmbeddingFunc, semantic *SemanticConfig, phrases []string) ([][]float32, error) {
	for attempt := 0; ; attempt++ {
		if err := pacer.wait(ctx); err != nil {
			return nil, err
		}
		embeddings, err := embedBatch(ctx, semantic, phrases)
		if err == nil {
			return embeddings, nil
		}
		if !isWarmupRateLimited(err) || attempt >= warmupRateLimitMaxRetries {
			return nil, err
		}
		if err := sleepContext(ctx, warmupRateLimitRetryDelay); err != nil {
			return nil, err
		}
	}
}
