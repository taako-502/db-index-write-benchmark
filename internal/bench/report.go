package bench

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

//go:embed report.html
var reportHTML string

func ValidateReport(r Report) error {
	if r.SchemaVersion != 1 && r.SchemaVersion != 2 && r.SchemaVersion != 3 {
		return fmt.Errorf("unsupported schema version: %d", r.SchemaVersion)
	}
	if r.SchemaVersion < 3 {
		r.Results = append([]Result(nil), r.Results...)
		UpgradeReport(&r)
	}
	if err := r.Config.Validate(); err != nil {
		return err
	}
	allowed := map[Case]bool{}
	for _, k := range Plan(r.Config) {
		allowed[k] = true
	}
	seen := map[Case]bool{}
	for _, v := range r.Results {
		if !allowed[v.Case] || seen[v.Case] {
			return fmt.Errorf("unexpected or duplicate result: %+v", v.Case)
		}
		seen[v.Case] = true
		for _, f := range []float64{v.Seconds, v.WallSeconds, v.OpsPerSecond} {
			if f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("invalid measurement")
			}
		}
		if math.Abs(v.OpsPerSecond-float64(v.Size)/v.Seconds) > 1e-6*v.OpsPerSecond {
			return fmt.Errorf("inconsistent throughput")
		}
	}
	if r.Completed && len(r.Results) != len(allowed) {
		return fmt.Errorf("complete report is missing results")
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func UpgradeReport(r *Report) {
	if r.SchemaVersion == 1 {
		r.SchemaVersion = 2
		r.Config.Engines = []string{"mongodb"}
		for i := range r.Results {
			r.Results[i].Engine = "mongodb"
		}
	}
	if r.SchemaVersion == 2 {
		r.SchemaVersion = 3
		for i := range r.Results {
			if r.Results[i].Operation != "insert" {
				r.Results[i].PreloadSize = r.Results[i].Size
			}
		}
	}
}
func Save(dir string, r Report) error {
	// Copy before upgrading so callers retain ownership of their slice.
	r.Results = append([]Result(nil), r.Results...)
	UpgradeReport(&r)

	if err := ValidateReport(r); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, "results.json"), data); err != nil {
		return err
	}
	var csvBuf bytes.Buffer
	w := csv.NewWriter(&csvBuf)
	if err = w.Write([]string{"engine", "preload_size", "size", "additional_indexes", "operation", "trial", "driver_seconds", "wall_seconds", "ops_per_second", "index_bytes"}); err != nil {
		return err
	}
	for _, v := range r.Results {
		if err = w.Write([]string{v.Engine, strconv.Itoa(v.PreloadSize), strconv.Itoa(v.Size), strconv.Itoa(v.Indexes), v.Operation, strconv.Itoa(v.Trial), strconv.FormatFloat(v.Seconds, 'g', -1, 64), strconv.FormatFloat(v.WallSeconds, 'g', -1, 64), strconv.FormatFloat(v.OpsPerSecond, 'g', -1, 64), strconv.FormatInt(v.IndexBytes, 10)}); err != nil {
			return err
		}
	}
	w.Flush()
	if err = w.Error(); err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, "results.csv"), csvBuf.Bytes()); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Report  Report    `json:"report"`
		Summary []Summary `json:"summary"`
	}{r, Summaries(r.Results)})
	if err != nil {
		return err
	}
	t, err := template.New("report").Parse(reportHTML)
	if err != nil {
		return err
	}
	var html bytes.Buffer
	// json.Marshal escapes HTML-sensitive characters including </script>.
	if err = t.Execute(&html, template.JS(payload)); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "report.html"), html.Bytes())
}
