package model

import (
	"fmt"
	"strings"
	"time"
)

// Alert is the Prometheus → Alertmanager v2 alert payload.
type Alert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt,omitempty"`
	EndsAt       time.Time         `json:"endsAt,omitempty"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
	Fingerprint  string            `json:"fingerprint,omitempty"`
}

// IsExpired reports whether endsAt is set to a real time in the past.
// Alertmanager uses zero / year-1 for "still firing".
func (a Alert) IsExpired(now time.Time) bool {
	if a.EndsAt.IsZero() || a.EndsAt.Year() <= 1 {
		return false
	}
	return !a.EndsAt.After(now)
}

// Extract returns the string value at a dotted path, or ("", false) if missing.
// Supported: labels.*, annotations.*, generatorURL, startsAt, endsAt, fingerprint.
func (a Alert) Extract(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	parts := strings.Split(path, ".")
	switch parts[0] {
	case "labels":
		if len(parts) != 2 || parts[1] == "" {
			return "", false
		}
		if a.Labels == nil {
			return "", false
		}
		v, ok := a.Labels[parts[1]]
		return v, ok
	case "annotations":
		if len(parts) != 2 || parts[1] == "" {
			return "", false
		}
		if a.Annotations == nil {
			return "", false
		}
		v, ok := a.Annotations[parts[1]]
		return v, ok
	case "generatorURL":
		if len(parts) != 1 {
			return "", false
		}
		if a.GeneratorURL == "" {
			return "", false
		}
		return a.GeneratorURL, true
	case "fingerprint":
		if len(parts) != 1 {
			return "", false
		}
		if a.Fingerprint == "" {
			return "", false
		}
		return a.Fingerprint, true
	case "startsAt":
		if len(parts) != 1 {
			return "", false
		}
		if a.StartsAt.IsZero() {
			return "", false
		}
		return a.StartsAt.UTC().Format(time.RFC3339Nano), true
	case "endsAt":
		if len(parts) != 1 {
			return "", false
		}
		if a.EndsAt.IsZero() {
			return "", false
		}
		return a.EndsAt.UTC().Format(time.RFC3339Nano), true
	default:
		return "", false
	}
}

// Stringify is a helper for non-string values in tests / future extensions.
func Stringify(v any) string {
	return fmt.Sprint(v)
}
