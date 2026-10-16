package custody

import (
	"testing"
	"time"

	"github.com/SethDS/am-proxy/internal/model"
)

func TestPartitionReadyExpires(t *testing.T) {
	now := time.Now()
	items := []Item{
		{Alert: model.Alert{EndsAt: now.Add(-time.Minute)}, EnqueuedAt: now},
		{Alert: model.Alert{Labels: map[string]string{"a": "1"}}, EnqueuedAt: now},
		{Alert: model.Alert{Labels: map[string]string{"a": "2"}}, NextAttempt: now.Add(time.Hour), EnqueuedAt: now},
	}
	ready, remaining, expired, wait := partitionReady(items, 10, now)
	if expired != 1 {
		t.Fatalf("expired=%d", expired)
	}
	if len(ready) != 1 || ready[0].Alert.Labels["a"] != "1" {
		t.Fatalf("ready=%+v", ready)
	}
	if len(remaining) != 1 || wait <= 0 {
		t.Fatalf("remaining=%+v wait=%v", remaining, wait)
	}
}

func TestQueueCapacity(t *testing.T) {
	q := newQueue("b", 2)
	if err := q.push([]Item{{Alert: model.Alert{}}, {Alert: model.Alert{}}}); err != nil {
		t.Fatal(err)
	}
	if err := q.push([]Item{{Alert: model.Alert{}}}); err != errFull {
		t.Fatalf("want errFull, got %v", err)
	}
}
