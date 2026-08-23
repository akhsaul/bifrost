package circuitbreaker

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

var (
	// Matches duration strings like "try again in 200ms", "retry after 5m", "resets in 4h30m", "wait in 10s"
	retryInPattern = regexp.MustCompile(`(?i)(?:retry|try again|resets?|wait)\s+(?:in|after)\s+((?:\d+(?:\.\d+)?\s*[a-zA-Z]+\s*)+)`)
	// Matches raw seconds: e.g. "Please retry after 3600 seconds"
	retrySecondsPattern = regexp.MustCompile(`(?i)retry(?:-after)?\s*(?:in|after)?\s*[:=]?\s*(\d+)\s*(?:s|sec|seconds)?`)
	// Matches timestamp or date: e.g. "reset at 2026-08-23T15:00:00Z"
	resetAtPattern = regexp.MustCompile(`(?i)(?:resets?|available)\s+at\s+([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:?[0-9]{2})?)`)
)

// IsRateLimitOrQuotaExceeded checks if the error indicates a rate limit or quota exhaustion.
func IsRateLimitOrQuotaExceeded(bifrostErr *schemas.BifrostError, customPatterns []*regexp.Regexp) bool {
	if bifrostErr == nil {
		return false
	}

	// Check status code 429
	if bifrostErr.StatusCode != nil && *bifrostErr.StatusCode == 429 {
		return true
	}

	errMsg := strings.ToLower(bifrostErr.GetErrorString())
	errCode := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Code != nil {
		errCode = strings.ToLower(*bifrostErr.Error.Code)
	}
	errType := ""
	if bifrostErr.Type != nil {
		errType = strings.ToLower(*bifrostErr.Type)
	}
	if bifrostErr.Error != nil && bifrostErr.Error.Type != nil {
		errType += " " + strings.ToLower(*bifrostErr.Error.Type)
	}

	// Common standard keywords for rate limit and quota issues
	keywords := []string{
		"rate_limit",
		"rate limit",
		"quota_exceeded",
		"quota exceeded",
		"insufficient_quota",
		"insufficient quota",
		"resource_exhausted",
		"resourceexhausted",
		"usage limit",
		"usage_limit",
		"too many requests",
		"free tier limit",
		"subscription limit",
		"plan limit",
		"exceeded your current quota",
		"check your plan and billing",
	}

	combined := errMsg + " " + errCode + " " + errType

	for _, kw := range keywords {
		if strings.Contains(combined, kw) {
			return true
		}
	}

	for _, cp := range customPatterns {
		if cp.MatchString(combined) {
			return true
		}
	}

	return false
}

// ParseCooldownDuration extracts cooldown duration from error details or returns (0, false) if not found.
func ParseCooldownDuration(bifrostErr *schemas.BifrostError, now time.Time) (time.Duration, bool) {
	if bifrostErr == nil {
		return 0, false
	}

	msg := bifrostErr.GetErrorString()

	// 1. Check formatted duration e.g. "try again in 5m30s", "wait in 4h", "try again in 200ms"
	if matches := retryInPattern.FindStringSubmatch(msg); len(matches) > 1 {
		raw := strings.ToLower(matches[1])
		// Replace words with units
		raw = regexp.MustCompile(`milliseconds?|millisecs?|millis?`).ReplaceAllString(raw, "ms")
		raw = regexp.MustCompile(`seconds?|secs?`).ReplaceAllString(raw, "s")
		raw = regexp.MustCompile(`minutes?|mins?`).ReplaceAllString(raw, "m")
		raw = regexp.MustCompile(`hours?|hrs?`).ReplaceAllString(raw, "h")
		raw = strings.ReplaceAll(raw, " ", "")

		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d, true
		}
	}

	// 2. Check ISO reset timestamp e.g. "resets at 2026-08-23T15:00:00Z"
	if matches := resetAtPattern.FindStringSubmatch(msg); len(matches) > 1 {
		if t, err := time.Parse(time.RFC3339, matches[1]); err == nil {
			if diff := t.Sub(now); diff > 0 {
				return diff, true
			}
		}
	}

	// 3. Check raw seconds pattern e.g. "retry after 120 seconds"
	if matches := retrySecondsPattern.FindStringSubmatch(msg); len(matches) > 1 {
		if sec, err := strconv.ParseInt(matches[1], 10, 64); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second, true
		}
	}

	return 0, false
}
