package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"dictum/api/internal/http/router"
	"dictum/api/internal/jobs"
	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

func main() {
	addr := os.Getenv("DICTUM_API_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgresql://dictum:dictum@localhost:5432/dictum"
	}
	mlURL := os.Getenv("ML_WORKER_URL")
	if mlURL == "" {
		mlURL = "http://localhost:8000"
	}

	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer db.Close()

	ml := mlclient.New(mlURL)
	queue := jobs.NewQueue(ml, db, 4)

	mux := router.New(router.Deps{Store: db, Jobs: queue, ML: ml})

	log.Printf("dictum-api listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
