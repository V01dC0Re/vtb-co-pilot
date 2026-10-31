package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"

	"vtb-copilot-backend/internal/handler"
	"vtb-copilot-backend/internal/llm"
	"vtb-copilot-backend/internal/middleware"
)

// loadDotEnv finds the nearest .env, including the repository root when run from backend/.
// Environment variables supplied by the process (e.g. Docker) take precedence.
func loadDotEnv() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		path := filepath.Join(dir, ".env")
		if _, err := os.Stat(path); err == nil {
			if err := godotenv.Load(path); err != nil {
				// Parser errors may contain secrets; do not include their contents in logs.
				return errors.New("не удалось загрузить .env: проверьте доступ и синтаксис файла")
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func main() {
	if err := loadDotEnv(); err != nil {
		log.Fatal(err)
	}
	model, err := llm.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	h, err := handler.NewWithGenerator("", model)
	if err != nil {
		log.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(middleware.CORS)
	r.Get("/", h.Root)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/cases", h.GetDemoCases)
		r.Get("/rules", h.GetRules)
		r.Get("/rules/{id}", h.GetRule)
		r.Post("/search", h.SearchChunks)
		r.Post("/copilot/resolve", h.ResolveCopilot)
		r.Post("/sandbox/evaluate", h.EvaluateSandbox)
	})

	server := &http.Server{
		Addr:              ":8000",
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("API слушает %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Ошибка завершения API: %v", err)
	}
}
