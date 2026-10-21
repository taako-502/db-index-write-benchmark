package web

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/taako-502/mongodb-index-write-benchmark/internal/bench"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControlLifecycleAndOrigin(t *testing.T) {
	started := make(chan struct{})
	s := &server{root: t.TempDir(), ctx: context.Background()}
	s.run = func(ctx context.Context, _, _ string, c bench.Config, save func(bench.Report) error) (bench.Report, error) {
		r := bench.Report{SchemaVersion: 3, Config: c, Environment: map[string]string{}}
		if e := save(r); e != nil {
			return r, e
		}
		close(started)
		<-ctx.Done()
		return r, ctx.Err()
	}
	handler := s.handler("127.0.0.1:8080")
	request := func(method, path, origin, header, host string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, bytes.NewReader(body))
		r.Host = host
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Bench-Request", header)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	c := bench.Config{Engines: []string{"postgresql"}, Sizes: []int{23}, PreloadSizes: []int{100000000}, Indexes: []int{0, 1, 5}, Repeats: 1, Batch: 7, Timeout: time.Second}
	body, _ := json.Marshal(c)
	for _, input := range [][3]string{{"http://evil.example", "1", "127.0.0.1:8080"}, {"", "", "127.0.0.1:8080"}, {"", "1", "evil.example:8080"}} {
		if w := request("POST", "/api/jobs", input[0], input[1], input[2], body); w.Code != 403 {
			t.Fatalf("unsafe request accepted: %d", w.Code)
		}
	}
	call := func(method, path string, body []byte) *httptest.ResponseRecorder {
		return request(method, path, "http://127.0.0.1:8080", "1", "127.0.0.1:8080", body)
	}
	if w := call("POST", "/api/jobs", []byte(`{"sizes":[0]}`)); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/jobs", body); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	if w := call("POST", "/api/jobs", body); w.Code != 409 {
		t.Fatal("concurrent job allowed")
	}
	if w := call("POST", "/api/cancel", nil); w.Code != 202 {
		t.Fatal(w.Code)
	}
	s.wg.Wait()
	j := s.jobs[0]
	if j.State != "cancelled" || !j.ReportReady {
		t.Fatalf("bad state: %+v", j)
	}
	data, e := os.ReadFile(filepath.Join(s.root, j.ID, "results.json"))
	if e != nil {
		t.Fatal(e)
	}
	var r bench.Report
	json.Unmarshal(data, &r)
	if len(r.Config.PreloadSizes) != 1 || r.Config.PreloadSizes[0] != 100000000 {
		t.Fatal("preload setting lost")
	}
	if r.Completed {
		t.Fatal("cancelled marked complete")
	}
	if w := call("GET", "/results/"+j.ID+"/report.html", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call("GET", "/results/"+j.ID+"/server.go", nil); w.Code != 404 {
		t.Fatal("unexpected file access")
	}
}
