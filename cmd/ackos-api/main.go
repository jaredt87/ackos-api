package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jaredt87/ackos-api/internal/httpapi"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	repositories := make(map[string]string)
	if raw := strings.TrimSpace(os.Getenv("ACKOS_REPOSITORIES")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &repositories); err != nil {
			return fmt.Errorf("parse ACKOS_REPOSITORIES: %w", err)
		}
	}
	secret := os.Getenv("ACKOS_API_TOKEN_SECRET")
	cfg := httpapi.Config{
		Repositories: repositories,
		BearerToken:  os.Getenv("ACKOS_API_BEARER_TOKEN"),
		SigningKey:   []byte(secret),
		AuthorityTTL: 30 * time.Second,
	}
	api, err := httpapi.New(cfg)
	if err != nil {
		return fmt.Errorf("configure API: %w", err)
	}
	addr := strings.TrimSpace(os.Getenv("ACKOS_API_ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
