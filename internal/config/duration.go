package config

import (
	"fmt"
	"time"
)

// Duration wraps time.Duration for YAML strings like "200ms", "5s", "24h".
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		if s == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := unmarshal(&n); err != nil {
		return err
	}
	*d = Duration(n)
	return nil
}
