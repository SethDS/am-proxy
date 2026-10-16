package match

import (
	"testing"

	"github.com/SethDS/am-proxy/internal/config"
	"github.com/SethDS/am-proxy/internal/model"
)

func TestFirstMatchWinsAndContinue(t *testing.T) {
	cfg := &config.Config{
		Unmatched:      "default",
		DefaultBackend: "default",
		Backends: map[string]config.Backend{
			"default": {URLs: []string{"http://d"}},
			"sre":     {URLs: []string{"http://s"}},
		},
		Rules: []config.Rule{
			{
				Match:  []config.Matcher{{Path: "labels.alertname", Op: "exact", Value: "Watchdog"}},
				Action: "drop",
			},
			{
				Match:    []config.Matcher{{Path: "labels.severity", Op: "regex", Value: "page|critical"}},
				Action:   "route",
				URL:      "http://pager",
				Continue: true,
			},
			{
				Match:   []config.Matcher{{Path: "labels.team", Op: "exact", Value: "sre"}},
				Action:  "route",
				Backend: "sre",
			},
		},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}

	d := eng.Evaluate(model.Alert{Labels: map[string]string{"alertname": "Watchdog"}})
	if !d.Drop {
		t.Fatal("expected drop")
	}

	d = eng.Evaluate(model.Alert{Labels: map[string]string{
		"severity": "critical",
		"team":     "sre",
	}})
	if d.Drop || len(d.Dests) != 2 {
		t.Fatalf("want fan-out 2 dests, got drop=%v dests=%+v", d.Drop, d.Dests)
	}
	if d.Dests[0].URL != "http://pager" || d.Dests[1].Backend != "sre" {
		t.Fatalf("dests=%+v", d.Dests)
	}

	d = eng.Evaluate(model.Alert{Labels: map[string]string{"severity": "warning"}})
	if d.Drop || len(d.Dests) != 1 || d.Dests[0].Backend != "default" {
		t.Fatalf("unmatched default: %+v", d)
	}
}

func TestRegexAnchored(t *testing.T) {
	cfg := &config.Config{
		Unmatched:      "drop",
		DefaultBackend: "",
		Backends:       map[string]config.Backend{},
		Rules: []config.Rule{{
			Match:  []config.Matcher{{Path: "labels.alertname", Op: "regex", Value: "CPU"}},
			Action: "drop",
		}},
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Unanchored would match HighCPU; we require full match.
	d := eng.Evaluate(model.Alert{Labels: map[string]string{"alertname": "HighCPU"}})
	if d.Drop && d.Rule == 0 {
		t.Fatal("partial regex should not match when anchored")
	}
	d = eng.Evaluate(model.Alert{Labels: map[string]string{"alertname": "CPU"}})
	if !d.Drop {
		t.Fatal("exact full-string regex should match")
	}
}
