package main

import (
	"context"
	"log"
	"net/http"
	"pet/postgres"
	"pet/server"
)

func main() {
	db := postgres.CreateDatabase(context.Background(), "postgres://pguser:pgpass@localhost:5432/pgdb?sslmode=disable")
	defer db.Pool.Close()
	server.HandlePool(db)
	if err := http.ListenAndServe(":556", nil); err != nil {
		log.Fatal(err)
	}

}
