package custody

import (
	"time"

	"github.com/SethDS/am-proxy/internal/model"
)

// Item is one alert held in custody for a destination.
type Item struct {
	Alert       model.Alert
	EnqueuedAt  time.Time
	Attempts    int
	NotFoundTries int
	NextAttempt time.Time
	HoldUntil   time.Time
}

type DestSpec struct {
	Key     string
	Backend string // named backend or empty
	URLs    []string
	HAMode  string
	Timeout time.Duration
}
