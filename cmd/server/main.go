package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/ccawmiku/webdav-cold-backup/internal/api"
	"github.com/ccawmiku/webdav-cold-backup/internal/service"
)

func main() {
	config := service.Config{
		ConfigDir:   env("WCB_CONFIG_DIR", "/config"),
		CacheDir:    env("WCB_CACHE_DIR", "/cache"),
		SourceRoot:  env("WCB_SOURCE_ROOT", "/sources"),
		RestoreRoot: env("WCB_RESTORE_ROOT", "/restore"),
	}
	app, err := service.New(config)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	handler := api.NewServer(app, config.SourceRoot, config.RestoreRoot)
	server := &http.Server{
		Addr: env("WCB_LISTEN_ADDR", "0.0.0.0:8080"), Handler: handler,
		ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopped
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("WebDAV Cold Backup listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
