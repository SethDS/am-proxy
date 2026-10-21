package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRegisterRuntimeIdempotent(t *testing.T) {
	RegisterRuntime()
	RegisterRuntime() // must not panic

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sawGo, sawProcess bool
	for _, mf := range mfs {
		switch mf.GetName() {
		case "go_goroutines":
			sawGo = true
		case "process_cpu_seconds_total":
			sawProcess = true
		}
	}
	if !sawGo {
		t.Fatal("missing go_goroutines after RegisterRuntime")
	}
	if !sawProcess {
		t.Fatal("missing process_cpu_seconds_total after RegisterRuntime")
	}
}
