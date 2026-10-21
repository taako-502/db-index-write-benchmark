package bench

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func smallConfig() Config {
	return Config{Sizes: []int{23}, Indexes: []int{0, 1, 5}, Repeats: 2, Batch: 7, Seed: 42, Timeout: 30 * time.Second}
}

func TestPlanBalancedAndDeterministic(t *testing.T) {
	c := smallConfig()
	p := Plan(c)
	if !reflect.DeepEqual(p, Plan(c)) {
		t.Fatal("plan is not deterministic")
	}
	seen := map[Case]bool{}
	for _, k := range p {
		if seen[k] {
			t.Fatalf("duplicate %+v", k)
		}
		seen[k] = true
	}
	if len(p) != 24 {
		t.Fatalf("got %d cases", len(p))
	}
	c.Seed++
	if reflect.DeepEqual(p, Plan(c)) {
		t.Fatal("seed does not affect order")
	}
	for _, k := range p[:12] {
		if k.Trial != 1 {
			t.Fatal("rounds mixed")
		}
	}
}

func TestDocumentsAndUpdates(t *testing.T) {
	a, e := Documents(23, 42)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := Documents(23, 42)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("data not deterministic")
	}
	for i, raw := range a {
		var d bson.M
		if e = bson.Unmarshal(raw.(bson.Raw), &d); e != nil {
			t.Fatal(e)
		}
		if d["_id"] != int64(i) || d["counter"] != int64(0) {
			t.Fatal(d)
		}
	}
	for op, n := range map[string]int{"update_unindexed": 1, "update_one": 1, "update_five": 5} {
		if len(UpdateFields(op)) != n {
			t.Fatal(op)
		}
	}
}

func sampleReport() Report {
	c := smallConfig()
	r := Report{SchemaVersion: 2, Config: c, Completed: true, Environment: map[string]string{"mongodb": "test fixture"}}
	for _, k := range Plan(c) {
		seconds := float64(k.Indexes+1) * float64(k.Trial)
		r.Results = append(r.Results, Result{Case: k, Seconds: seconds, WallSeconds: seconds, OpsPerSecond: float64(k.Size) / seconds})
	}
	return r
}

func TestSummaryAndValidation(t *testing.T) {
	r := sampleReport()
	if e := ValidateReport(r); e != nil {
		t.Fatal(e)
	}
	for _, s := range Summaries(r.Results) {
		if s.Samples != 2 || s.Ratio != float64(s.Indexes+1) || s.MedianSeconds != 1.5*float64(s.Indexes+1) {
			t.Fatalf("bad summary %+v", s)
		}
	}
	r.Results = r.Results[:1]
	if ValidateReport(r) == nil {
		t.Fatal("incomplete data accepted as complete")
	}
	r.Completed = false
	if e := ValidateReport(r); e != nil {
		t.Fatal(e)
	}
	r.Results[0].Seconds = math.NaN()
	if ValidateReport(r) == nil {
		t.Fatal("NaN accepted")
	}
}

func TestSaveEscapesHTMLAndRoundTrips(t *testing.T) {
	r := sampleReport()
	r.Config.Notes = "</script><script>alert('x')</script>"
	dir := t.TempDir()
	if e := Save(dir, r); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "report.html"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), r.Config.Notes) {
		t.Fatal("script injection in report")
	}
	data, e := os.ReadFile(filepath.Join(dir, "results.json"))
	if e != nil {
		t.Fatal(e)
	}
	var got Report
	if e = json.Unmarshal(data, &got); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, r) {
		t.Fatal("JSON did not round-trip")
	}
	start := strings.Index(string(b), `<script id="payload" type="application/json">`)
	if start < 0 {
		t.Fatal("missing embedded data")
	}
	payload := string(b)[start:]
	payload = payload[strings.Index(payload, ">")+1:]
	payload = payload[:strings.Index(payload, "</script>")]
	if !json.Valid([]byte(payload)) {
		t.Fatal("embedded data is not JSON")
	}
	r.Completed = false
	r.Results = nil
	if e := Save(dir, r); e != nil {
		t.Fatal(e)
	}
}

func TestConfigRejectsInvalidInputs(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.Indexes = []int{1} }, func(c *Config) { c.Sizes = []int{0} }, func(c *Config) { c.Batch = 0 }, func(c *Config) { c.Indexes = []int{0, 0} }, func(c *Config) { c.Indexes = []int{0, 6} }, func(c *Config) { c.Repeats = 0 }} {
		c := smallConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

// Opt-in integration test; only randomly named databases created by Run are used.
func TestMongoIntegration(t *testing.T) {
	uri := os.Getenv("BENCH_TEST_URI")
	if uri == "" {
		t.Skip("set BENCH_TEST_URI to run against MongoDB")
	}
	c := smallConfig()
	dir := t.TempDir()
	checkpoints := 0
	r, e := Run(context.Background(), uri, c, func(r Report) error { checkpoints++; return Save(dir, r) })
	if e != nil {
		t.Fatal(e)
	}
	if !r.Completed || len(r.Results) != 24 || checkpoints != 26 {
		t.Fatalf("unexpected completion: %d results, %d checkpoints", len(r.Results), checkpoints)
	}
	if e = ValidateReport(r); e != nil {
		t.Fatal(e)
	}
	for _, v := range r.Results {
		if v.IndexBytes <= 0 {
			t.Fatal("missing index stats")
		}
	}
}

func TestEngineBaselinesAreSeparate(t *testing.T) {
	r := sampleReport()
	r.Config.Engines = []string{"mongodb", "postgresql"}
	for _, v := range append([]Result(nil), r.Results...) {
		v.Engine = "postgresql"
		v.Seconds *= 10
		v.WallSeconds *= 10
		v.OpsPerSecond /= 10
		r.Results = append(r.Results, v)
	}
	if err := ValidateReport(r); err != nil {
		t.Fatal(err)
	}
	for _, s := range Summaries(r.Results) {
		if s.Ratio != float64(s.Indexes+1) {
			t.Fatalf("cross-engine baseline: %+v", s)
		}
	}
	r.Results[0].Engine = "unknown"
	if ValidateReport(r) == nil {
		t.Fatal("accepted unknown engine")
	}
}

func TestLegacyReportUpgrade(t *testing.T) {
	r := sampleReport()
	r.SchemaVersion = 1
	r.Config.Engines = nil
	for i := range r.Results {
		r.Results[i].Engine = ""
	}
	if e := ValidateReport(r); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e := Save(dir, r); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "results.json"))
	var got Report
	if e := json.Unmarshal(b, &got); e != nil {
		t.Fatal(e)
	}
	if got.SchemaVersion != 2 || got.Results[0].Engine != "mongodb" {
		t.Fatal("legacy report not upgraded")
	}
	if r.Results[0].Engine != "" {
		t.Fatal("Save mutated caller data")
	}
}
