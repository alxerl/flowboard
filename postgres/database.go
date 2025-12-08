package postgres

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Database struct {
	Pool *pgxpool.Pool
	ctx  context.Context
}

func CreateDatabase(ctx context.Context, dsn string) Database {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal("pool create error:", err)
	}
	if err := pool.Ping(ctx); err != nil {
		log.Fatal(err)
	}
	const schema = `
	CREATE TABLE if not exists tasks (
		id SERIAL PRIMARY KEY,
		name TEXT,
		status TEXT
	);`
	if _, err := pool.Exec(ctx, schema); err != nil {
		log.Fatal("create table error:", err)
	}
	fmt.Println("table ready")
	return Database{pool, ctx}
}
