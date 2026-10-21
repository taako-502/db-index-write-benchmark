package bench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	mathrand "math/rand"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// RunPostgres creates and drops only its own random schema in the configured DB.
func RunPostgres(ctx context.Context, uri string, c Config, checkpoint func(Report) error) (report Report, err error) {
	c.Engines = []string{"postgresql"}
	if err = c.Validate(); err != nil {
		return
	}
	cfg, e := pgx.ParseConfig(uri)
	if e != nil {
		return report, fmt.Errorf("invalid PostgreSQL connection configuration")
	}
	cfg.ConnectTimeout = 10 * time.Second
	cfg.RuntimeParams["application_name"] = "index-write-benchmark"
	cfg.RuntimeParams["synchronous_commit"] = "off"
	if c.Journal {
		cfg.RuntimeParams["synchronous_commit"] = "on"
	}
	// Prefer primary-key lookup; this is recorded, not presented as a universal DB tuning.
	cfg.RuntimeParams["enable_seqscan"] = "off"
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return report, fmt.Errorf("connect PostgreSQL: %w", e)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
	}()
	var token [12]byte
	if _, err = rand.Read(token[:]); err != nil {
		return
	}
	schema := "index_write_bench_" + hex.EncodeToString(token[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if err = withTimeout(ctx, c.Timeout, func(ctx context.Context) error { _, e := conn.Exec(ctx, "CREATE SCHEMA "+quoted); return e }); err != nil {
		return
	}
	log.Printf("temporary PostgreSQL schema: %s", schema)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Cancellation can close the measurement connection. Cleanup owns a fresh connection.
		cleanupConn, e := pgx.ConnectConfig(cleanup, cfg)
		if e == nil {
			_, e = cleanupConn.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE")
			_ = cleanupConn.Close(cleanup)
		}
		if e != nil {
			log.Printf("cleanup failed for schema %s", schema)
			if err == nil {
				err = fmt.Errorf("schema cleanup: %w", e)
			}
		}
	}()
	env := map[string]string{"engine": "postgresql", "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "schema": schema, "concurrency": "1", "client_cpus": strconv.Itoa(runtime.NumCPU()), "gomaxprocs": strconv.Itoa(runtime.GOMAXPROCS(0)), "batch_api": "pgx SendBatch: one statement per row, implicit transaction per batch; Close includes commit acknowledgement", "timing": "sum of SendBatch through Close; batch preparation excluded", "cache_policy": "fresh table; measured operations after preload; no cache flush", "planner": "enable_seqscan=off (preference, not a forced plan)", "maintenance": "autovacuum settings unchanged; background maintenance may affect timings"}
	for _, name := range []string{"server_version", "synchronous_commit", "fsync", "full_page_writes", "shared_buffers", "wal_level", "autovacuum"} {
		var value string
		if err = withTimeout(ctx, c.Timeout, func(ctx context.Context) error { return conn.QueryRow(ctx, "SHOW "+name).Scan(&value) }); err != nil {
			return
		}
		env[name] = value
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/jackc/pgx/v5" {
				env["pgx"] = dep.Version
			}
		}
	}
	fill := c.Fillfactor
	if fill == 0 {
		fill = 100
	}
	env["fillfactor"] = strconv.Itoa(fill)
	return runPlan(ctx, c, env, func(ctx context.Context, c Config, k Case, ids []int) (Result, error) {
		return pgCase(ctx, conn, quoted, c, k, ids, fill)
	}, checkpoint)
}

func pgCase(ctx context.Context, conn *pgx.Conn, schema string, c Config, k Case, ids []int, fill int) (Result, error) {
	table := schema + ".documents"
	exec := func(sql string) error {
		return withTimeout(ctx, c.Timeout, func(ctx context.Context) error { _, e := conn.Exec(ctx, sql); return e })
	}
	if e := exec("DROP TABLE IF EXISTS " + table); e != nil {
		return Result{}, e
	}
	if e := exec(fmt.Sprintf("CREATE TABLE %s (id bigint PRIMARY KEY, counter bigint NOT NULL, field1 bigint NOT NULL, field2 bigint NOT NULL, field3 bigint NOT NULL, field4 bigint NOT NULL, field5 bigint NOT NULL) WITH (fillfactor=%d)", table, fill)); e != nil {
		return Result{}, e
	}
	for i := 1; i <= k.Indexes; i++ {
		if e := exec(fmt.Sprintf("CREATE INDEX ON %s (field%d)", table, i)); e != nil {
			return Result{}, e
		}
	}
	rng := mathrand.New(mathrand.NewSource(c.Seed))
	insert := "INSERT INTO " + table + " (id,counter,field1,field2,field3,field4,field5) VALUES ($1,$2,$3,$4,$5,$6,$7)"
	if e := streamDocuments(ctx, k.PreloadSize, c.Batch, rng, func(docs []any) error {
		_, err := sendPG(ctx, conn, c.Timeout, pgInsertBatch(insert, docs))
		return err
	}); e != nil {
		return Result{}, e
	}
	if k.PreloadSize > 0 {
		if e := exec("ANALYZE " + table); e != nil {
			return Result{}, e
		}
	}
	update := ""
	if k.Operation != "insert" {
		var assignments []string
		for _, field := range UpdateFields(k.Operation) {
			assignments = append(assignments, field.Key+"="+field.Key+"+1")
		}
		update = "UPDATE " + table + " SET " + strings.Join(assignments, ",") + " WHERE id=$1"
	}
	if c.Cooldown > 0 {
		timer := time.NewTimer(c.Cooldown)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	runtime.GC()
	wallStart := time.Now()
	var elapsed time.Duration
	for start := 0; start < k.Size; start += c.Batch {
		end := start + min(c.Batch, k.Size-start)
		batch := &pgx.Batch{}
		if k.Operation == "insert" {
			docs, e := documentBatch(k.PreloadSize+start, end-start, rng)
			if e != nil {
				return Result{}, e
			}
			batch = pgInsertBatch(insert, docs)
		} else {
			for _, id := range ids[start:end] {
				batch.Queue(update, int64(id))
			}
		}
		d, e := sendPG(ctx, conn, c.Timeout, batch)
		if e != nil {
			return Result{}, e
		}
		elapsed += d
	}
	wall := time.Since(wallStart)
	var count, indexBytes int64
	where := ""
	if k.Operation == "update_unindexed" {
		where = " WHERE counter=1"
	}
	if e := withTimeout(ctx, c.Timeout, func(ctx context.Context) error {
		return conn.QueryRow(ctx, "SELECT count(*) FROM "+table+where).Scan(&count)
	}); e != nil {
		return Result{}, e
	}
	expected := k.PreloadSize
	if k.Operation == "insert" {
		expected += k.Size
	}
	if k.Operation == "update_unindexed" {
		expected = k.Size
	}
	if count != int64(expected) {
		return Result{}, fmt.Errorf("row count: want %d got %d", expected, count)
	}
	if e := withTimeout(ctx, c.Timeout, func(ctx context.Context) error {
		return conn.QueryRow(ctx, "SELECT pg_indexes_size($1::regclass)", table).Scan(&indexBytes)
	}); e != nil {
		return Result{}, e
	}
	seconds := elapsed.Seconds()
	return Result{Case: k, Seconds: seconds, WallSeconds: wall.Seconds(), OpsPerSecond: float64(k.Size) / seconds, IndexBytes: indexBytes}, nil
}

func sendPG(ctx context.Context, conn *pgx.Conn, timeout time.Duration, batch *pgx.Batch) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	results := conn.SendBatch(ctx, batch)
	for range batch.QueuedQueries {
		tag, e := results.Exec()
		if e != nil {
			_ = results.Close()
			return 0, e
		}
		if tag.RowsAffected() != 1 {
			_ = results.Close()
			return 0, fmt.Errorf("expected one affected row, got %d", tag.RowsAffected())
		}
	}
	e := results.Close()
	return time.Since(start), e
}

func pgInsertBatch(sql string, docs []any) *pgx.Batch {
	batch := &pgx.Batch{}
	for _, d := range docs {
		raw := d.(bson.Raw)
		batch.Queue(sql, raw.Lookup("_id").Int64(), raw.Lookup("counter").Int64(), raw.Lookup("field1").Int64(), raw.Lookup("field2").Int64(), raw.Lookup("field3").Int64(), raw.Lookup("field4").Int64(), raw.Lookup("field5").Int64())
	}
	return batch
}
