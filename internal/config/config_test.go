package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadPassThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "am-proxy.yml")
	content := `
listen: ":9094"
backends:
  default:
    urls: ["http://alertmanager:9093"]
unmatched: default
default_backend: default
rules: []
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Queue.WorkerCount != 8 {
		t.Fatalf("worker default = %d", cfg.Queue.WorkerCount)
	}
	if cfg.Backends["default"].HAMode != "all" {
		t.Fatalf("ha_mode = %q", cfg.Backends["default"].HAMode)
	}
}

func TestValidateRouteNeedsTarget(t *testing.T) {
	cfg := Defaults()
	cfg.Backends = map[string]Backend{"default": {URLs: []string{"http://x"}}}
	cfg.DefaultBackend = "default"
	cfg.Rules = []Rule{{
		Match:  []Matcher{{Path: "labels.a", Op: "exact", Value: "1"}},
		Action: "route",
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for route without backend/url")
	}
}

func TestLoadRootExampleDurations(t *testing.T) {
	cfg, err := Load("../../am-proxy.yml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Queue.RetryInitial.Duration() != 200*time.Millisecond {
		t.Fatalf("RetryInitial=%v", cfg.Queue.RetryInitial.Duration())
	}
	if cfg.Queue.MaxAlertAge.Duration() != 24*time.Hour {
		t.Fatalf("MaxAlertAge=%v", cfg.Queue.MaxAlertAge.Duration())
	}
	if len(cfg.Rules) < 3 {
		t.Fatalf("rules=%d", len(cfg.Rules))
	}
}
