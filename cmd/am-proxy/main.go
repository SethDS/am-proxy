package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SethDS/am-proxy/internal/api"
	"github.com/SethDS/am-proxy/internal/config"
	"github.com/SethDS/am-proxy/internal/custody"
	"github.com/SethDS/am-proxy/internal/match"
	"github.com/SethDS/am-proxy/internal/metrics"
	"github.com/SethDS/am-proxy/internal/obs"
)

func main() {
	configFile := flag.String("config.file", "am-proxy.yml", "Path to am-proxy.yml")
	listen := flag.String("listen-address", "", "Listen address (overrides config)")
	flag.Parse()

	metrics.RegisterRuntime()

	cfg, err := config.Load(*configFile)
	if err != nil {
		panic(err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}

	log := obs.NewLogger(cfg.LogLevel)
	engine, err := match.NewEngine(cfg)
	if err != nil {
		log.Error("compile rules", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mgr := custody.NewManager(cfg, log)
	mgr.Start(ctx)
	defer mgr.Stop()

	srvAPI := &api.Server{
		Log:         log,
		Engine:      engine,
		Custody:     mgr,
		MaxInflight: cfg.Queue.MaxInflightRequests,
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srvAPI.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("am-proxy listening", "addr", cfg.Listen, "config", *configFile)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server failed", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	log.Info("am-proxy stopped")
}
