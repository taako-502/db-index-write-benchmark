package bench

import (
	"context"
	"fmt"
	"time"
)

// RunSuite runs engines sequentially in the requested order to avoid resource contention.
// Each engine uses the same seed and condition order; engine order is part of Config.
func RunSuite(ctx context.Context, mongoURI, pgURI string, c Config, checkpoint func(Report) error) (Report, error) {
	r := Report{SchemaVersion: 2, StartedAt: time.Now().UTC(), Config: c, Environment: map[string]string{}, Results: []Result{}}
	if e := c.Validate(); e != nil {
		return r, e
	}
	if e := checkpoint(r); e != nil {
		return r, e
	}
	for _, engine := range c.EngineNames() {
		prefix := len(r.Results)
		save := func(part Report) error {
			r.Results = append(r.Results[:prefix], part.Results...)
			for k, v := range part.Environment {
				r.Environment[engine+"."+k] = v
			}
			return checkpoint(r)
		}
		var e error
		if engine == "mongodb" {
			_, e = Run(ctx, mongoURI, c, save)
		} else {
			_, e = RunPostgres(ctx, pgURI, c, save)
		}
		if e != nil {
			return r, fmt.Errorf("%s: %w", engine, e)
		}
	}
	r.Completed = true
	return r, checkpoint(r)
}
