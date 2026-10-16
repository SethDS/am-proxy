package match

import (
	"fmt"
	"regexp"

	"github.com/SethDS/am-proxy/internal/config"
	"github.com/SethDS/am-proxy/internal/model"
)

type ActionKind int

const (
	ActionDrop ActionKind = iota
	ActionRoute
)

type Destination struct {
	// Backend is a named backend from config, or empty for inline URL.
	Backend string
	// URL is an inline Alertmanager base URL (no path), or empty when Backend is set.
	URL string
	// DestKey uniquely identifies the custody queue (backend name or "url:"+URL).
	DestKey string
}

type Decision struct {
	Drop  bool
	Rule  int // index of drop rule, or -1
	Dests []Destination
}

type compiledMatcher struct {
	path  string
	exact bool
	re    *regexp.Regexp
	value string
}

type compiledRule struct {
	matchers []compiledMatcher
	action   ActionKind
	backend  string
	url      string
	cont     bool
	index    int
}

// Engine evaluates ordered rules (first-match-wins + continue fan-out).
type Engine struct {
	rules          []compiledRule
	unmatchedDrop  bool
	defaultBackend string
}

func NewEngine(cfg *config.Config) (*Engine, error) {
	e := &Engine{
		unmatchedDrop:  cfg.Unmatched == "drop",
		defaultBackend: cfg.DefaultBackend,
	}
	for i, r := range cfg.Rules {
		cr := compiledRule{index: i, cont: r.Continue}
		switch r.Action {
		case "drop":
			cr.action = ActionDrop
		case "route":
			cr.action = ActionRoute
			cr.backend = r.Backend
			cr.url = r.URL
		}
		for _, m := range r.Match {
			cm := compiledMatcher{path: m.Path}
			switch m.Op {
			case "exact":
				cm.exact = true
				cm.value = m.Value
			case "regex":
				// Anchor automatically: full-string match.
				re, err := regexp.Compile("^(?:" + m.Value + ")$")
				if err != nil {
					return nil, fmt.Errorf("rules[%d] path %q: invalid regex: %w", i, m.Path, err)
				}
				cm.re = re
			}
			cr.matchers = append(cr.matchers, cm)
		}
		e.rules = append(e.rules, cr)
	}
	return e, nil
}

func (e *Engine) Evaluate(a model.Alert) Decision {
	d := Decision{Rule: -1}
	routed := false

	for _, r := range e.rules {
		if !r.matches(a) {
			continue
		}
		if r.action == ActionDrop {
			d.Drop = true
			d.Rule = r.index
			d.Dests = nil
			return d
		}
		// route
		dest := destinationFor(r)
		d.Dests = append(d.Dests, dest)
		routed = true
		if !r.cont {
			return d
		}
		// continue: keep evaluating for additional routes / later drop
	}

	if routed {
		return d
	}
	if e.unmatchedDrop {
		d.Drop = true
		d.Rule = -1
		return d
	}
	d.Dests = []Destination{{
		Backend: e.defaultBackend,
		DestKey: e.defaultBackend,
	}}
	return d
}

func (r compiledRule) matches(a model.Alert) bool {
	for _, m := range r.matchers {
		v, ok := a.Extract(m.path)
		if !ok {
			return false
		}
		if m.exact {
			if v != m.value {
				return false
			}
			continue
		}
		if !m.re.MatchString(v) {
			return false
		}
	}
	return true
}

func destinationFor(r compiledRule) Destination {
	if r.url != "" {
		return Destination{
			URL:     r.url,
			DestKey: "url:" + r.url,
		}
	}
	return Destination{
		Backend: r.backend,
		DestKey: r.backend,
	}
}
