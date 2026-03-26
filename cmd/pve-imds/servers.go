package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"
)

// pprofOption returns an fx.Option that serves pprof endpoints on addr.
func pprofOption(addr string) fx.Option {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return httpServerOption(addr, "pprof", mux)
}

// metricsOption returns an fx.Option that serves Prometheus metrics on addr.
func metricsOption(addr string) fx.Option {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return httpServerOption(addr, "metrics", mux)
}

// httpServerOption returns an fx.Option that binds an HTTP server to addr and
// manages its lifecycle via fx. label is used only in error messages.
func httpServerOption(addr, label string, mux *http.ServeMux) fx.Option {
	return fx.Invoke(func(lc fx.Lifecycle) {
		srv := &http.Server{Addr: addr, Handler: mux}
		lc.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
				if err != nil {
					return fmt.Errorf("%s listen %s: %w", label, addr, err)
				}
				go func() { _ = srv.Serve(ln) }()
				return nil
			},
			OnStop: func(ctx context.Context) error {
				return srv.Shutdown(ctx)
			},
		})
	})
}
