package bench

import (
	"fmt"
	"math/rand"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var Operations = []string{"insert", "update_unindexed", "update_one", "update_five"}

type Config struct {
	Sizes    []int         `json:"sizes"`
	Indexes  []int         `json:"indexes"`
	Repeats  int           `json:"repeats"`
	Batch    int           `json:"batch_size"`
	Seed     int64         `json:"seed"`
	Journal  bool          `json:"journal"`
	Timeout  time.Duration `json:"operation_timeout_ns"`
	Cooldown time.Duration `json:"cooldown_ns"`
	Notes    string        `json:"notes"`
}

func (c Config) Validate() error {
	if len(c.Sizes) == 0 || len(c.Indexes) == 0 || c.Repeats < 1 || c.Batch < 1 || c.Batch > 100000 || c.Timeout <= 0 || c.Cooldown < 0 {
		return fmt.Errorf("sizes/indexes must be nonempty, repeats >= 1, batch 1..100000, timeout > 0, cooldown >= 0")
	}
	if !slices.Contains(c.Indexes, 0) {
		return fmt.Errorf("indexes must include 0 for the baseline")
	}
	for _, values := range [][]int{c.Sizes, c.Indexes} {
		seen := map[int]bool{}
		for _, v := range values {
			if seen[v] {
				return fmt.Errorf("duplicate condition: %d", v)
			}
			seen[v] = true
		}
	}
	for _, n := range c.Sizes {
		if n < 1 {
			return fmt.Errorf("sizes must be positive")
		}
	}
	for _, n := range c.Indexes {
		if n < 0 || n > 5 {
			return fmt.Errorf("indexes must be in 0..5")
		}
	}
	return nil
}

type Case struct {
	Size      int    `json:"size"`
	Indexes   int    `json:"indexes"`
	Operation string `json:"operation"`
	Trial     int    `json:"trial"`
}

type Result struct {
	Case
	Seconds      float64 `json:"seconds"`
	WallSeconds  float64 `json:"wall_seconds"`
	OpsPerSecond float64 `json:"ops_per_second"`
	IndexBytes   int64   `json:"index_bytes"`
}

type Report struct {
	SchemaVersion int               `json:"schema_version"`
	StartedAt     time.Time         `json:"started_at"`
	Completed     bool              `json:"completed"`
	Config        Config            `json:"config"`
	Environment   map[string]string `json:"environment"`
	Results       []Result          `json:"results"`
}

// Each round contains all conditions once, in a reproducibly shuffled order.
func Plan(c Config) []Case {
	rng := rand.New(rand.NewSource(c.Seed))
	var plan []Case
	for trial := 1; trial <= c.Repeats; trial++ {
		var round []Case
		for _, n := range c.Sizes {
			for _, idx := range c.Indexes {
				for _, op := range Operations {
					round = append(round, Case{n, idx, op, trial})
				}
			}
		}
		rng.Shuffle(len(round), func(i, j int) { round[i], round[j] = round[j], round[i] })
		plan = append(plan, round...)
	}
	return plan
}

// Raw BSON avoids counting data generation/initial BSON encoding in insert timing.
func Documents(n int, seed int64) ([]any, error) {
	rng := rand.New(rand.NewSource(seed))
	docs := make([]any, n)
	for i := range docs {
		d := bson.D{{Key: "_id", Value: int64(i)}, {Key: "counter", Value: int64(0)}}
		for f := 1; f <= 5; f++ {
			d = append(d, bson.E{Key: fmt.Sprintf("field%d", f), Value: rng.Int63n(1_000_000_000)})
		}
		raw, err := bson.Marshal(d)
		if err != nil {
			return nil, err
		}
		docs[i] = bson.Raw(raw)
	}
	return docs, nil
}

func UpdateFields(op string) bson.D {
	switch op {
	case "update_unindexed":
		return bson.D{{Key: "counter", Value: int64(1)}}
	case "update_one":
		return bson.D{{Key: "field1", Value: int64(1)}}
	case "update_five":
		var fields bson.D
		for f := 1; f <= 5; f++ {
			fields = append(fields, bson.E{Key: fmt.Sprintf("field%d", f), Value: int64(1)})
		}
		return fields
	default:
		panic("unknown update operation: " + op)
	}
}

func UpdateModels(ids []int, op string) []mongo.WriteModel {
	models := make([]mongo.WriteModel, len(ids))
	update := bson.D{{Key: "$inc", Value: UpdateFields(op)}}
	for i, id := range ids {
		models[i] = mongo.NewUpdateOneModel().SetFilter(bson.D{{Key: "_id", Value: int64(id)}}).SetUpdate(update).SetHint("_id_")
	}
	return models
}

type Summary struct {
	Size          int     `json:"size"`
	Indexes       int     `json:"indexes"`
	Operation     string  `json:"operation"`
	Samples       int     `json:"samples"`
	MedianSeconds float64 `json:"median_seconds"`
	MinSeconds    float64 `json:"min_seconds"`
	MaxSeconds    float64 `json:"max_seconds"`
	MedianOps     float64 `json:"median_ops"`
	MinOps        float64 `json:"min_ops"`
	MaxOps        float64 `json:"max_ops"`
	Ratio         float64 `json:"ratio"`
}

func median(v []float64) float64 {
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

func Summaries(results []Result) []Summary {
	type key struct {
		n, idx int
		op     string
	}
	groups := map[key][]Result{}
	for _, r := range results {
		k := key{r.Size, r.Indexes, r.Operation}
		groups[k] = append(groups[k], r)
	}
	var out []Summary
	for k, rs := range groups {
		times, ops := make([]float64, len(rs)), make([]float64, len(rs))
		for i, r := range rs {
			times[i] = r.Seconds
			ops[i] = r.OpsPerSecond
		}
		mt, mo := median(times), median(ops)
		out = append(out, Summary{Size: k.n, Indexes: k.idx, Operation: k.op, Samples: len(rs), MedianSeconds: mt, MinSeconds: times[0], MaxSeconds: times[len(times)-1], MedianOps: mo, MinOps: ops[0], MaxOps: ops[len(ops)-1]})
	}
	for i := range out {
		for _, b := range out {
			if b.Size == out[i].Size && b.Operation == out[i].Operation && b.Indexes == 0 {
				out[i].Ratio = out[i].MedianSeconds / b.MedianSeconds
			}
		}
	}
	slices.SortFunc(out, func(a, b Summary) int {
		if a.Size != b.Size {
			return a.Size - b.Size
		}
		if a.Operation < b.Operation {
			return -1
		}
		if a.Operation > b.Operation {
			return 1
		}
		return a.Indexes - b.Indexes
	})
	return out
}
