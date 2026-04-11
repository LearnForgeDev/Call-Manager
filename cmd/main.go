package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"

	sig "github.com/learnforge/signalingserver/internal/signal"
)

func main() {
	addr := getenv("ADDR", ":8777")
	redisUrl := getenv("REDIS_URL", "redis://localhost:6379/0")
	pgUrl := getenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/learnforge?sslmode=disable")
	lkURL := getenv("LIVEKIT_URL", "wss://my-livekit-url")
	lkKey := getenv("LIVEKIT_API_KEY", "devkey")
	lkSecret := getenv("LIVEKIT_API_SECRET", "secret")
	coturnURL := getenv("COTURN_URL", "") // e.g. turn:turn.my-domain.com:3478
	coturnSecret := getenv("COTURN_SECRET", "")
	log.Printf("Starting Signaling Server on %s", addr)

	// Redis Init
	opt, err := redis.ParseURL(redisUrl)
	if err != nil {
		log.Fatalf("Failed to parse redis url: %v", err)
	}
	rdb := redis.NewClient(opt)

	// Postgres Init
	db, err := sql.Open("postgres", pgUrl)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	// Ensure the whiteboards table exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS whiteboards (
			room_id VARCHAR(255) PRIMARY KEY,
			state JSONB NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatalf("Failed to create whiteboards table: %v", err)
	}

	hub, err := sig.NewHub(rdb, db, lkURL, lkKey, lkSecret, coturnURL, coturnSecret)
	if err != nil {
		log.Fatalf("Failed to create hub: %v", err)
	}

	if err := hub.Run(); err != nil {
		log.Fatalf("Failed to run hub: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", hub.WebsocketHandler())
	
	// API Endpoint for generating Room IDs
	mux.HandleFunc("/api/rooms/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		
		// Typically, your main backend handles room authorization, but this serves as a utility.
		roomID := uuid.New().String()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"roomId": roomID})
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server ListenAndServe error: %v", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := hub.Shutdown(shutdownCtx); err != nil {
		log.Printf("Hub Shutdown error: %v", err)
	}

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server Shutdown error: %v", err)
	} else {
		log.Println("Server gracefully stopped")
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}