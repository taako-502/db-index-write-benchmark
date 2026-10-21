package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/taako-502/mongodb-index-write-benchmark/internal/bench"
	"github.com/taako-502/mongodb-index-write-benchmark/internal/web"
)

func integers(s string) ([]int, error) {
	var v []int
	for _, part := range strings.Split(s, ",") {
		n, e := strconv.Atoi(strings.TrimSpace(part))
		if e != nil {
			return nil, e
		}
		v = append(v, n)
	}
	return v, nil
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	var c bench.Config
	engines := flag.String("engines", "mongodb", "comma-separated engines: mongodb,postgresql (runs sequentially in this order)")
	serve := flag.Bool("serve", false, "start local configuration and execution UI")
	listen := flag.String("listen", "127.0.0.1:8080", "loopback address for UI")
	flag.IntVar(&c.Fillfactor, "pg-fillfactor", 100, "PostgreSQL table fillfactor (10..100)")
	sizes := flag.String("sizes", "100000,1000000", "comma-separated document counts")
	indexes := flag.String("indexes", "0,1,5", "additional index counts (0..5, must include 0)")
	flag.IntVar(&c.Repeats, "repeats", 5, "measured trials per condition")
	flag.IntVar(&c.Batch, "batch", 1000, "documents per acknowledged batch (1 = single-document batch)")
	flag.Int64Var(&c.Seed, "seed", 42, "reproducible data/order seed")
	flag.BoolVar(&c.Journal, "journal", false, "wait for durable acknowledgement (MongoDB j:true / PostgreSQL synchronous_commit:on)")
	flag.DurationVar(&c.Timeout, "timeout", 2*time.Minute, "timeout per operation/setup call")
	flag.DurationVar(&c.Cooldown, "cooldown", time.Second, "pause after setup per trial")
	flag.StringVar(&c.Notes, "notes", "", "machine, Docker resources, disk and other environment notes")
	out := flag.String("out", "", "new output directory (default results/<UTC timestamp>)")
	input := flag.String("report", "", "regenerate report from results.json without MongoDB")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	var err error
	c.Engines = strings.Split(*engines, ",")
	for i := range c.Engines {
		c.Engines[i] = strings.TrimSpace(c.Engines[i])
	}
	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://127.0.0.1:27018"
	}
	pgURI := os.Getenv("POSTGRES_URI")
	if pgURI == "" {
		pgURI = "postgres://benchmark:benchmark@127.0.0.1:5433/benchmark?sslmode=disable"
	}
	if *serve {
		if *input != "" {
			return fmt.Errorf("-serve cannot be combined with -report")
		}
		root := *out
		if root == "" {
			root = "results"
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return web.Serve(ctx, *listen, root, mongoURI, pgURI)
	}
	if c.Sizes, err = integers(*sizes); err != nil {
		return fmt.Errorf("sizes: %w", err)
	}
	if c.Indexes, err = integers(*indexes); err != nil {
		return fmt.Errorf("indexes: %w", err)
	}
	if err = c.Validate(); err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join("results", time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	if err = os.MkdirAll(filepath.Dir(*out), 0755); err != nil {
		return err
	}
	if err = os.Mkdir(*out, 0755); err != nil {
		return fmt.Errorf("output directory must be new: %w", err)
	}
	if *input != "" {
		data, e := os.ReadFile(*input)
		if e != nil {
			return e
		}
		var r bench.Report
		if e = json.Unmarshal(data, &r); e != nil {
			return e
		}
		bench.UpgradeReport(&r)
		if e = bench.ValidateReport(r); e != nil {
			return e
		}
		if e = bench.Save(*out, r); e != nil {
			return e
		}
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if _, err = bench.RunSuite(ctx, mongoURI, pgURI, c, func(r bench.Report) error { return bench.Save(*out, r) }); err != nil {
			return err
		}
	}
	fmt.Printf("Report: %s\n", filepath.Join(*out, "report.html"))
	return nil
}
