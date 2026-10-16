package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen         string             `yaml:"listen"`
	LogLevel       string             `yaml:"log_level"`
	Queue          QueueConfig        `yaml:"queue"`
	Backends       map[string]Backend `yaml:"backends"`
	Unmatched      string             `yaml:"unmatched"` // default | drop
	DefaultBackend string             `yaml:"default_backend"`
	Rules          []Rule             `yaml:"rules"`
	Forward        ForwardConfig      `yaml:"forward"`
}

type QueueConfig struct {
	MaxAlertsPerBackend int      `yaml:"max_alerts_per_backend"`
	MaxInflightRequests int      `yaml:"max_inflight_requests"`
	WorkerCount         int      `yaml:"worker_count"`
	OutboundBatchSize   int      `yaml:"outbound_batch_size"`
	RetryInitial        Duration `yaml:"retry_initial"`
	RetryMax            Duration `yaml:"retry_max"`
	MaxRetries          int      `yaml:"max_retries"`
	MaxAlertAge         Duration `yaml:"max_alert_age"`
	Retry404Max         int      `yaml:"retry_404_max"`
}

type ForwardConfig struct {
	// DeadLetterCap bounds retained dead-letter / auth-hold alerts in memory.
	DeadLetterCap int `yaml:"dead_letter_cap"`
}

type Backend struct {
	URLs    []string `yaml:"urls"`
	HAMode  string   `yaml:"ha_mode"` // all | any
	Timeout Duration `yaml:"timeout"`
}

type Rule struct {
	Match    []Matcher `yaml:"match"`
	Action   string    `yaml:"action"` // drop | route
	Backend  string    `yaml:"backend,omitempty"`
	URL      string    `yaml:"url,omitempty"`
	Continue bool      `yaml:"continue,omitempty"`
}

type Matcher struct {
	Path  string `yaml:"path"`
	Op    string `yaml:"op"` // exact | regex
	Value string `yaml:"value"`
}

func Defaults() Config {
	return Config{
		Listen:   ":9094",
		LogLevel: "info",
		Queue: QueueConfig{
			MaxAlertsPerBackend: 50000,
			MaxInflightRequests: 512,
			WorkerCount:         8,
			OutboundBatchSize:   200,
			RetryInitial:        Duration(200 * time.Millisecond),
			RetryMax:            Duration(30 * time.Second),
			MaxRetries:          50,
			MaxAlertAge:         Duration(24 * time.Hour),
			Retry404Max:         3,
		},
		Backends:       map[string]Backend{},
		Unmatched:      "default",
		DefaultBackend: "",
		Rules:          nil,
		Forward: ForwardConfig{
			DeadLetterCap: 10000,
		},
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := Defaults()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Listen == "" {
		c.Listen = ":9094"
	}
	if c.Queue.MaxAlertsPerBackend <= 0 {
		return fmt.Errorf("queue.max_alerts_per_backend must be > 0")
	}
	if c.Queue.MaxInflightRequests <= 0 {
		return fmt.Errorf("queue.max_inflight_requests must be > 0")
	}
	if c.Queue.WorkerCount <= 0 {
		return fmt.Errorf("queue.worker_count must be > 0")
	}
	if c.Queue.OutboundBatchSize <= 0 {
		return fmt.Errorf("queue.outbound_batch_size must be > 0")
	}
	if c.Queue.RetryInitial <= 0 {
		c.Queue.RetryInitial = Duration(200 * time.Millisecond)
	}
	if c.Queue.RetryMax <= 0 {
		c.Queue.RetryMax = Duration(30 * time.Second)
	}
	if c.Queue.MaxRetries <= 0 {
		c.Queue.MaxRetries = 50
	}
	if c.Queue.MaxAlertAge <= 0 {
		c.Queue.MaxAlertAge = Duration(24 * time.Hour)
	}
	if c.Queue.Retry404Max <= 0 {
		c.Queue.Retry404Max = 3
	}
	if c.Forward.DeadLetterCap <= 0 {
		c.Forward.DeadLetterCap = 10000
	}

	switch c.Unmatched {
	case "", "default":
		c.Unmatched = "default"
	case "drop":
	default:
		return fmt.Errorf("unmatched must be default or drop")
	}

	if c.Unmatched == "default" {
		if c.DefaultBackend == "" {
			return fmt.Errorf("default_backend required when unmatched=default")
		}
		if _, ok := c.Backends[c.DefaultBackend]; !ok {
			return fmt.Errorf("default_backend %q not found in backends", c.DefaultBackend)
		}
	}

	for name, b := range c.Backends {
		if len(b.URLs) == 0 {
			return fmt.Errorf("backend %q has no urls", name)
		}
		switch b.HAMode {
		case "", "all":
			b.HAMode = "all"
		case "any":
		default:
			return fmt.Errorf("backend %q ha_mode must be all or any", name)
		}
		if b.Timeout <= 0 {
			b.Timeout = Duration(5 * time.Second)
		}
		c.Backends[name] = b
	}

	for i, r := range c.Rules {
		switch r.Action {
		case "drop":
		case "route":
			if r.Backend == "" && r.URL == "" {
				return fmt.Errorf("rules[%d]: route requires backend or url", i)
			}
			if r.Backend != "" {
				if _, ok := c.Backends[r.Backend]; !ok {
					return fmt.Errorf("rules[%d]: backend %q not found", i, r.Backend)
				}
			}
		default:
			return fmt.Errorf("rules[%d]: action must be drop or route", i)
		}
		if len(r.Match) == 0 {
			return fmt.Errorf("rules[%d]: match is required", i)
		}
		for j, m := range r.Match {
			if m.Path == "" {
				return fmt.Errorf("rules[%d].match[%d]: path required", i, j)
			}
			switch m.Op {
			case "exact", "regex":
			default:
				return fmt.Errorf("rules[%d].match[%d]: op must be exact or regex", i, j)
			}
			if m.Op == "regex" && m.Value == "" {
				return fmt.Errorf("rules[%d].match[%d]: regex value required", i, j)
			}
		}
	}
	return nil
}
