package bench

import (
	"context"
	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"os"
	"testing"
)

func TestPostgresIntegration(t *testing.T) {
	uri := os.Getenv("BENCH_TEST_POSTGRES_URI")
	if uri == "" {
		t.Skip("set BENCH_TEST_POSTGRES_URI")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	docs, _ := Documents(23, 42)
	checks := 0
	r, err := RunPostgres(ctx, uri, smallConfig(), func(r Report) error {
		if e := ValidateReport(r); e != nil {
			return e
		}
		if len(r.Results) == 0 || r.Completed {
			return nil
		}
		checks++
		last := r.Results[len(r.Results)-1]
		table := pgx.Identifier{r.Environment["schema"], "documents"}.Sanitize()
		rows, e := conn.Query(ctx, "SELECT id,counter,field1,field2,field3,field4,field5 FROM "+table+" ORDER BY id")
		if e != nil {
			return e
		}
		defer rows.Close()
		i := 0
		for rows.Next() {
			var v [7]int64
			if e = rows.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6]); e != nil {
				return e
			}
			if i >= len(docs) {
				t.Fatal("extra row")
			}
			raw := docs[i].(bson.Raw)
			for f, name := range []string{"_id", "counter", "field1", "field2", "field3", "field4", "field5"} {
				want := raw.Lookup(name).Int64()
				if last.Operation != "insert" {
					for _, field := range UpdateFields(last.Operation) {
						if field.Key == name {
							want++
						}
					}
				}
				if v[f] != want {
					t.Fatalf("%s row %d %s: got %d want %d", last.Operation, i, name, v[f], want)
				}
			}
			i++
		}
		if i != 23 {
			t.Fatalf("got %d rows", i)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Completed || checks != 24 {
		t.Fatalf("incomplete: %d", checks)
	}
	var count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_namespace WHERE nspname=$1", r.Environment["schema"]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("schema not cleaned: %d %v", count, err)
	}
}
