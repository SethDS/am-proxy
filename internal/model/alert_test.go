package model

import (
	"testing"
	"time"
)

func TestExtract(t *testing.T) {
	a := Alert{
		Labels:       map[string]string{"severity": "critical"},
		Annotations:  map[string]string{"summary": "hi"},
		GeneratorURL: "http://prom/graph",
		Fingerprint:  "abc",
		StartsAt:     time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC),
	}
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"labels.severity", "critical", true},
		{"annotations.summary", "hi", true},
		{"generatorURL", "http://prom/graph", true},
		{"fingerprint", "abc", true},
		{"labels.missing", "", false},
		{"labels", "", false},
		{"unknown", "", false},
		{"startsAt", "2026-10-16T21:00:00Z", true},
	}
	for _, tc := range cases {
		got, ok := a.Extract(tc.path)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("Extract(%q)=(%q,%v) want (%q,%v)", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Date(2026, 10, 16, 22, 0, 0, 0, time.UTC)
	if (Alert{}).IsExpired(now) {
		t.Fatal("zero endsAt should not expire")
	}
	if (Alert{EndsAt: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)}).IsExpired(now) {
		t.Fatal("year-1 endsAt should not expire")
	}
	past := Alert{EndsAt: now.Add(-time.Minute)}
	if !past.IsExpired(now) {
		t.Fatal("past endsAt should expire")
	}
	future := Alert{EndsAt: now.Add(time.Minute)}
	if future.IsExpired(now) {
		t.Fatal("future endsAt should not expire")
	}
}
