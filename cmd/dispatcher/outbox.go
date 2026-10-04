package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/postgres"
	"github.com/redis/go-redis/v9"
)

const publicationTimeout = 2 * time.Second

func dispatcherRedisOptions(addr string) *redis.Options {
	return &redis.Options{
		Addr: addr, MaxRetries: -1, ContextTimeoutEnabled: true,
		DialTimeout: publicationTimeout, ReadTimeout: publicationTimeout, WriteTimeout: publicationTimeout,
	}
}

// Equal jitter: 0.5–1s, 1–2s, 2–4s, ... 15–30s. The persisted
// publication failure count, not the device attempt count, selects the window.
func publicationBackoff(failures int) time.Duration {
	ceiling := time.Second << min(max(failures, 0), 5)
	ceiling = min(ceiling, 30*time.Second)
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}

func processOutboxBatch(ctx context.Context, db *postgres.Store, rdb *redis.Client, log *slog.Logger) error {
	followup, err := processOutboxPass(ctx, db, rdb, log)
	if err != nil {
		return err
	}
	if followup {
		// A created-command transaction can make a delivery intent eligible.
		// One bounded follow-up pass reads that committed intent without waiting
		// for another ticker interval. Never publish a process-local envelope.
		_, err = processOutboxPass(ctx, db, rdb, log)
	}
	return err
}

func processOutboxPass(ctx context.Context, db *postgres.Store, rdb *redis.Client, log *slog.Logger) (bool, error) {
	followup := false
	items, err := db.ClaimOutbox(ctx, 64)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		// Cancellation must not charge unattempted rows for a transport failure.
		if err := ctx.Err(); err != nil {
			return false, err
		}
		itemCtx, stop := context.WithTimeout(ctx, publicationTimeout)
		err := publish(itemCtx, db, rdb, item)
		if err == nil {
			err = db.MarkOutboxPublished(itemCtx, item.ID, time.Now())
			if errors.Is(err, control.ErrNotFound) {
				err = nil
			}
		}
		stop()
		if err == nil && item.Kind == "command.created" {
			followup = true
		}
		if err != nil {
			log.Warn("publish or confirm outbox", "outbox_id", item.ID, "error", err)
			// Use the parent, not the expired per-item context. Confirmation
			// failures are ambiguous too: retain the same envelope and ID.
			delay := publicationBackoff(item.PublicationFailures)
			retryCtx, cancel := context.WithTimeout(ctx, time.Second)
			changed, deferErr := db.DeferOutbox(retryCtx, item, delay)
			cancel()
			if deferErr != nil {
				return false, fmt.Errorf("persist outbox retry: %w", deferErr)
			}
			if changed {
				log.Info("outbox retry scheduled", "outbox_id", item.ID, "retry_delay", delay)
			}
		}
	}
	return followup, nil
}

func outboxMetrics(db *postgres.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, stop := context.WithTimeout(r.Context(), time.Second)
		defer stop()
		stats, err := db.OutboxStats(ctx)
		if err != nil {
			// A failed scrape lets Prometheus mark up=0; never emit false zeros.
			http.Error(w, "outbox metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# HELP fleetrelay_up Dispatcher metrics endpoint is responding.\n# TYPE fleetrelay_up gauge\nfleetrelay_up 1\n"+
			"# HELP fleetrelay_outbox_pending Committed outbox rows not yet handled, including delayed retries.\n# TYPE fleetrelay_outbox_pending gauge\nfleetrelay_outbox_pending %d\n"+
			"# HELP fleetrelay_outbox_retry_waiting Unhandled outbox rows waiting for their next publication time.\n# TYPE fleetrelay_outbox_retry_waiting gauge\nfleetrelay_outbox_retry_waiting %d\n"+
			"# HELP outbox_oldest_unpublished_seconds Age of the oldest unhandled outbox row, including delayed retries.\n# TYPE outbox_oldest_unpublished_seconds gauge\noutbox_oldest_unpublished_seconds %.6f\n",
			stats.Pending, stats.RetryWaiting, stats.OldestSeconds)
	}
}
