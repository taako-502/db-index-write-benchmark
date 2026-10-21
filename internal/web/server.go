package web

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/taako-502/mongodb-index-write-benchmark/internal/bench"
)

//go:embed index.html
var page []byte

type runner func(context.Context, string, string, bench.Config, func(bench.Report) error) (bench.Report, error)
type job struct {
	ID          string       `json:"id"`
	State       string       `json:"state"`
	Done        int          `json:"done"`
	Total       int          `json:"total"`
	Error       string       `json:"error,omitempty"`
	ReportReady bool         `json:"report_ready"`
	Config      bench.Config `json:"config"`
	cancel      context.CancelFunc
}
type server struct {
	mu                    sync.Mutex
	jobs                  []*job
	active                *job
	root, mongoURI, pgURI string
	run                   runner
	ctx                   context.Context
	wg                    sync.WaitGroup
}

// Serve is intentionally loopback-only. It is a local control panel, not a shared service.
func Serve(ctx context.Context, addr, root, mongoURI, pgURI string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("listen host must be 127.0.0.1 or ::1")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(absolute, 0755); err != nil {
		return err
	}
	s := &server{root: absolute, mongoURI: mongoURI, pgURI: pgURI, ctx: ctx, run: bench.RunSuite}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.handler(listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		case <-stopped:
		}
	}()
	log.Printf("Open http://%s (results: %s)", listener.Addr(), absolute)
	err = srv.Serve(listener)
	s.mu.Lock()
	if s.active != nil {
		s.active.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *server) handler(host string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.jobs)
	})
	mux.HandleFunc("POST /api/jobs", s.start)
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active == nil {
			http.Error(w, "no active run", http.StatusConflict)
			return
		}
		s.active.State = "cancelling"
		s.active.cancel()
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /results/{id}/{file}", func(w http.ResponseWriter, r *http.Request) {
		id, file := r.PathValue("id"), r.PathValue("file")
		if file != "report.html" && file != "results.json" && file != "results.csv" {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		found := false
		for _, j := range s.jobs {
			if j.ID == id && j.ReportReady {
				found = true
				break
			}
		}
		s.mu.Unlock()
		if !found {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.root, id, file))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		// Host validation blocks DNS rebinding; a custom request header + Origin check blocks cross-site commands.
		if r.Host != host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("X-Bench-Request") != "1" || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host) {
				http.Error(w, "invalid request origin", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *server) start(w http.ResponseWriter, r *http.Request) {
	var c bench.Config
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&c); e != nil {
		http.Error(w, "invalid configuration JSON", 400)
		return
	}
	if e := c.Validate(); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	// Keep the browser control panel bounded. CLI supports larger intentional experiments.
	for _, n := range c.Sizes {
		if n > 10000000 {
			http.Error(w, "UI supports at most 10,000,000 rows per case", 400)
			return
		}
	}
	if c.Repeats > 100 || len(c.Sizes) > 10 {
		http.Error(w, "UI supports up to 100 repeats and 10 sizes", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		http.Error(w, "another run is active", 409)
		return
	}
	var token [12]byte
	if _, e := rand.Read(token[:]); e != nil {
		http.Error(w, "cannot create run ID", 500)
		return
	}
	id := hex.EncodeToString(token[:])
	dir := filepath.Join(s.root, id)
	if e := os.Mkdir(dir, 0755); e != nil {
		http.Error(w, "cannot create result directory", 500)
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	j := &job{ID: id, State: "running", Total: len(bench.Plan(c)), Config: c, cancel: cancel}
	s.jobs = append(s.jobs, j)
	s.active = j
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		_, e := s.run(ctx, s.mongoURI, s.pgURI, c, func(report bench.Report) error {
			if err := bench.Save(dir, report); err != nil {
				return err
			}
			s.mu.Lock()
			j.Done = len(report.Results)
			j.ReportReady = true
			s.mu.Unlock()
			return nil
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		s.active = nil
		if e != nil {
			j.State = "failed"
			if ctx.Err() != nil {
				j.State = "cancelled"
			}
			message := e.Error()
			for _, uri := range []string{s.mongoURI, s.pgURI} {
				if uri != "" {
					message = strings.ReplaceAll(message, uri, "[connection]")
				}
			}
			j.Error = message
		} else {
			j.State = "completed"
		}
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(j)
}
