package bench

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"math/rand"
	"os"
	"reflect"
	"testing"
)

func TestPreloadPlanAndRatios(t *testing.T) {
	c := smallConfig()
	c.PreloadSizes = []int{23, 101}
	c.Engines = []string{"mongodb", "postgresql"}
	r := Report{SchemaVersion: 3, Config: c, Completed: true}
	for _, k := range Plan(c) {
		seconds := float64(k.PreloadSize * (k.Indexes + 1) * k.Trial)
		r.Results = append(r.Results, Result{Case: k, Seconds: seconds, WallSeconds: seconds, OpsPerSecond: float64(k.Size) / seconds})
	}
	if len(r.Results) != 96 {
		t.Fatal(len(r.Results))
	}
	if e := ValidateReport(r); e != nil {
		t.Fatal(e)
	}
	for _, s := range Summaries(r.Results) {
		if s.Ratio != float64(s.Indexes+1) || s.Samples != 2 {
			t.Fatalf("mixed preload groups: %+v", s)
		}
	}
	r.Results[0].PreloadSize = 999
	if ValidateReport(r) == nil {
		t.Fatal("unexpected preload accepted")
	}
	for _, preloads := range [][]int{{0}, {-1}, {22}, {23, 23}, {int(^uint(0) >> 1)}} {
		c.PreloadSizes = preloads
		if c.Validate() == nil {
			t.Fatalf("accepted %v", preloads)
		}
	}
}

func TestSampleIDsAcrossPopulation(t *testing.T) {
	for _, n := range []int{23, 100000000} {
		ids := sampleIDs(n, 23, 42)
		if !reflect.DeepEqual(ids, sampleIDs(n, 23, 42)) {
			t.Fatal("not reproducible")
		}
		seen := map[int]bool{}
		above := false
		for _, id := range ids {
			if id < 0 || id >= n || seen[id] {
				t.Fatal("bad sample", ids)
			}
			seen[id] = true
			above = above || id >= 23
		}
		if n > 23 && !above {
			t.Fatal("sample restricted to first rows")
		}
	}
}

func TestStreamingFixtureAndAppend(t *testing.T) {
	all, _ := Documents(124, 42)
	for _, batch := range []int{1, 7, 101} {
		rng := rand.New(rand.NewSource(42))
		var got []any
		err := streamDocuments(context.Background(), 101, batch, rng, func(d []any) error {
			if len(d) > batch {
				t.Fatal("oversized batch")
			}
			got = append(got, d...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tail, _ := documentBatch(101, 23, rng)
		got = append(got, tail...)
		if !reflect.DeepEqual(all, got) {
			t.Fatal("batching changed fixture or append IDs")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if streamDocuments(ctx, 100, 7, rand.New(rand.NewSource(42)), func([]any) error { t.Fatal("called after cancel"); return nil }) == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestRunPlanPreloadAndWarmup(t *testing.T) {
	c := smallConfig()
	c.PreloadSizes = []int{100000000}
	c.Repeats = 1
	calls := 0
	r, e := runPlan(context.Background(), c, nil, func(ctx context.Context, c Config, k Case, ids []int) (Result, error) {
		calls++
		if k.Trial == 0 && k.PreloadSize > 1000 {
			t.Fatal("warmup used full preload")
		}
		if k.Trial > 0 && k.PreloadSize != 100000000 {
			t.Fatal("wrong population")
		}
		if k.Operation != "insert" && len(ids) != k.Size {
			t.Fatal("wrong sample size")
		}
		return Result{Case: k, Seconds: 1, WallSeconds: 1, OpsPerSecond: float64(k.Size)}, nil
	}, func(r Report) error { return ValidateReport(r) })
	if e != nil || !r.Completed || calls != 24 {
		t.Fatal(calls, e)
	}
}

func TestPreloadedDatabaseContents(t *testing.T) {
	for _, engine := range []string{"mongodb", "postgresql"} {
		t.Run(engine, func(t *testing.T) {
			uri := os.Getenv("BENCH_TEST_URI")
			if engine == "postgresql" {
				uri = os.Getenv("BENCH_TEST_POSTGRES_URI")
			}
			if uri == "" {
				t.Skip("database integration opt-in")
			}
			ctx := context.Background()
			c := smallConfig()
			c.PreloadSizes = []int{53, 101}
			c.Repeats = 1
			var read func(Report) ([]bson.M, error)
			var run func(context.Context, string, Config, func(Report) error) (Report, error)
			if engine == "mongodb" {
				client, e := mongo.Connect(options.Client().ApplyURI(uri))
				if e != nil {
					t.Fatal(e)
				}
				defer client.Disconnect(ctx)
				read = func(r Report) ([]bson.M, error) {
					cur, e := client.Database(r.Environment["database"]).Collection("documents").Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
					if e != nil {
						return nil, e
					}
					defer cur.Close(ctx)
					var out []bson.M
					e = cur.All(ctx, &out)
					return out, e
				}
				run = Run
			} else {
				conn, e := pgx.Connect(ctx, uri)
				if e != nil {
					t.Fatal(e)
				}
				defer conn.Close(ctx)
				read = func(r Report) ([]bson.M, error) {
					rows, e := conn.Query(ctx, "SELECT id,counter,field1,field2,field3,field4,field5 FROM "+pgx.Identifier{r.Environment["schema"], "documents"}.Sanitize()+" ORDER BY id")
					if e != nil {
						return nil, e
					}
					defer rows.Close()
					var out []bson.M
					for rows.Next() {
						var v [7]int64
						if e = rows.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6]); e != nil {
							return nil, e
						}
						d := bson.M{}
						for i, k := range []string{"_id", "counter", "field1", "field2", "field3", "field4", "field5"} {
							d[k] = v[i]
						}
						out = append(out, d)
					}
					return out, rows.Err()
				}
				run = RunPostgres
			}
			checks := 0
			r, e := run(ctx, uri, c, func(r Report) error {
				if e := ValidateReport(r); e != nil {
					return e
				}
				if len(r.Results) == 0 || r.Completed {
					return nil
				}
				checks++
				k := r.Results[len(r.Results)-1]
				n := k.PreloadSize
				if k.Operation == "insert" {
					n += k.Size
				}
				got, e := read(r)
				if e != nil {
					return e
				}
				if len(got) != n {
					return fmt.Errorf("got %d rows, want %d", len(got), n)
				}
				docs, _ := Documents(n, c.Seed)
				selected := map[int]bool{}
				if k.Operation != "insert" {
					for _, id := range sampleIDs(k.PreloadSize, k.Size, c.Seed) {
						selected[id] = true
					}
				}
				for i, raw := range docs {
					var want bson.M
					if e = bson.Unmarshal(raw.(bson.Raw), &want); e != nil {
						return e
					}
					if selected[i] {
						for _, field := range UpdateFields(k.Operation) {
							want[field.Key] = want[field.Key].(int64) + 1
						}
					}
					if !reflect.DeepEqual(got[i], want) {
						return fmt.Errorf("%+v row %d: got %v want %v", k, i, got[i], want)
					}
				}
				return nil
			})
			if e != nil || !r.Completed || checks != 24 {
				t.Fatalf("checks=%d error=%v", checks, e)
			}
		})
	}
}
