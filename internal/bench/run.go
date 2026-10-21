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
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

func withTimeout(ctx context.Context, d time.Duration, fn func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return fn(c)
}

// Run owns a cryptographically random database. It never accepts an existing
// database name and only drops the database it created during this invocation.
func Run(ctx context.Context, uri string, c Config, checkpoint func(Report) error) (report Report, err error) {
	if err = c.Validate(); err != nil {
		return report, err
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).
		SetAppName("mongodb-index-write-benchmark").SetMaxPoolSize(1).
		SetRetryWrites(false).SetRetryReads(false).
		SetWriteConcern(&writeconcern.WriteConcern{W: 1, Journal: &c.Journal}).
		SetServerSelectionTimeout(10 * time.Second))
	if err != nil {
		return report, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Disconnect(cleanup)
	}()
	if err = withTimeout(ctx, c.Timeout, func(ctx context.Context) error { return client.Ping(ctx, readpref.Primary()) }); err != nil {
		return report, err
	}
	var build struct {
		Version string `bson:"version"`
	}
	if err = withTimeout(ctx, c.Timeout, func(ctx context.Context) error {
		return client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&build)
	}); err != nil {
		return report, err
	}
	var token [12]byte
	if _, err = rand.Read(token[:]); err != nil {
		return report, err
	}
	db := client.Database("index_write_bench_" + hex.EncodeToString(token[:]))
	log.Printf("temporary database: %s", db.Name())
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := db.Drop(cleanup); e != nil {
			log.Printf("cleanup failed for %s: %v", db.Name(), e)
			if err == nil {
				err = fmt.Errorf("database cleanup: %w", e)
			}
		}
	}()
	env := map[string]string{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "client_cpus": strconv.Itoa(runtime.NumCPU()), "gomaxprocs": strconv.Itoa(runtime.GOMAXPROCS(0)), "mongodb": build.Version, "write_concern": fmt.Sprintf("w:1,j:%t", c.Journal), "concurrency": "1", "retry_writes": "false", "ordered": "true", "database": db.Name(), "timing": "sum of acknowledged driver calls; batch preparation excluded", "cache_policy": "fresh collection; updates immediately after preload; no OS/WiredTiger cache flush"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "go.mongodb.org/mongo-driver/v2" {
				env["mongo_driver"] = dep.Version
			}
		}
	}
	report = Report{SchemaVersion: 1, StartedAt: time.Now().UTC(), Config: c, Environment: env, Results: []Result{}}
	if err = checkpoint(report); err != nil {
		return report, err
	}
	// Exercise each operation/index combination, outside measured trials.
	warmSize := min(1000, slicesMin(c.Sizes))
	warmDocs, e := Documents(warmSize, c.Seed)
	if e != nil {
		return report, e
	}
	warmIDs := mathrand.New(mathrand.NewSource(c.Seed)).Perm(warmSize)
	log.Printf("warm-up: %d documents per condition", warmSize)
	for _, idx := range c.Indexes {
		for _, op := range Operations {
			if _, err = runCase(ctx, db, c, Case{warmSize, idx, op, 0}, warmDocs, warmIDs); err != nil {
				return report, fmt.Errorf("warm-up: %w", err)
			}
		}
	}
	var docs []any
	var ids []int
	lastSize := 0
	plan := Plan(c)
	for i, k := range plan {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		if k.Size != lastSize {
			docs = nil
			ids = nil
			docs, err = Documents(k.Size, c.Seed)
			if err != nil {
				return report, err
			}
			ids = mathrand.New(mathrand.NewSource(c.Seed)).Perm(k.Size)
			lastSize = k.Size
		}
		log.Printf("[%d/%d] n=%d indexes=%d op=%s trial=%d", i+1, len(plan), k.Size, k.Indexes, k.Operation, k.Trial)
		var result Result
		result, err = runCase(ctx, db, c, k, docs, ids)
		if err != nil {
			return report, fmt.Errorf("%+v: %w", k, err)
		}
		report.Results = append(report.Results, result)
		if err = checkpoint(report); err != nil {
			return report, err
		}
	}
	report.Completed = true
	err = checkpoint(report)
	return report, err
}

func slicesMin(v []int) int {
	m := v[0]
	for _, n := range v[1:] {
		m = min(m, n)
	}
	return m
}

func runCase(ctx context.Context, db *mongo.Database, c Config, k Case, docs []any, ids []int) (Result, error) {
	col := db.Collection("documents")
	if err := withTimeout(ctx, c.Timeout, func(ctx context.Context) error { return col.Drop(ctx) }); err != nil {
		return Result{}, err
	}
	if err := withTimeout(ctx, c.Timeout, func(ctx context.Context) error { return db.CreateCollection(ctx, col.Name()) }); err != nil {
		return Result{}, err
	}
	if k.Indexes > 0 {
		models := make([]mongo.IndexModel, k.Indexes)
		for i := range models {
			models[i] = mongo.IndexModel{Keys: bson.D{{Key: fmt.Sprintf("field%d", i+1), Value: 1}}}
		}
		if err := withTimeout(ctx, c.Timeout, func(ctx context.Context) error { _, err := col.Indexes().CreateMany(ctx, models); return err }); err != nil {
			return Result{}, err
		}
	}
	if k.Operation != "insert" {
		for start := 0; start < k.Size; start += c.Batch {
			batch := docs[start:min(start+c.Batch, k.Size)]
			if err := withTimeout(ctx, c.Timeout, func(ctx context.Context) error {
				_, err := col.InsertMany(ctx, batch, options.InsertMany().SetOrdered(true))
				return err
			}); err != nil {
				return Result{}, err
			}
		}
	}
	// The same pause occurs after setup in all conditions; it is not a cache reset.
	if c.Cooldown > 0 {
		timer := time.NewTimer(c.Cooldown)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	runtime.GC() // client collection is outside measured time, consistently per trial
	var elapsed time.Duration
	wallStart := time.Now()
	for start := 0; start < k.Size; start += c.Batch {
		end := min(start+c.Batch, k.Size)
		var models []mongo.WriteModel
		if k.Operation != "insert" {
			models = UpdateModels(ids[start:end], k.Operation)
		}
		batchCtx, cancel := context.WithTimeout(ctx, c.Timeout)
		began := time.Now()
		var callErr error
		var changed int64
		if k.Operation == "insert" {
			res, e := col.InsertMany(batchCtx, docs[start:end], options.InsertMany().SetOrdered(true))
			callErr = e
			if e == nil {
				changed = int64(len(res.InsertedIDs))
			}
		} else {
			res, e := col.BulkWrite(batchCtx, models, options.BulkWrite().SetOrdered(true))
			callErr = e
			if e == nil {
				changed = res.ModifiedCount
			}
		}
		elapsed += time.Since(began)
		cancel()
		if callErr != nil {
			return Result{}, callErr
		}
		if changed != int64(end-start) {
			return Result{}, fmt.Errorf("expected %d writes, got %d", end-start, changed)
		}
	}
	wall := time.Since(wallStart)
	// Verify collection size (and counter outcome), outside the timed region.
	// Each update batch also checks ModifiedCount against its expected size.
	filter := bson.D{}
	if k.Operation != "insert" {
		for _, f := range UpdateFields(k.Operation) {
			if f.Key == "counter" {
				filter = append(filter, bson.E{Key: "counter", Value: int64(1)})
			}
		}
	}
	var count int64
	if err := withTimeout(ctx, c.Timeout, func(ctx context.Context) error { var e error; count, e = col.CountDocuments(ctx, filter); return e }); err != nil {
		return Result{}, err
	}
	if count != int64(k.Size) {
		return Result{}, fmt.Errorf("count mismatch: want %d, got %d", k.Size, count)
	}
	var stats struct {
		TotalIndexSize int64 `bson:"totalIndexSize"`
	}
	if err := withTimeout(ctx, c.Timeout, func(ctx context.Context) error {
		return db.RunCommand(ctx, bson.D{{Key: "collStats", Value: col.Name()}}).Decode(&stats)
	}); err != nil {
		return Result{}, err
	}
	seconds := elapsed.Seconds()
	return Result{Case: k, Seconds: seconds, WallSeconds: wall.Seconds(), OpsPerSecond: float64(k.Size) / seconds, IndexBytes: stats.TotalIndexSize}, nil
}
