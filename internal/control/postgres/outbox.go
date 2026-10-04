package postgres

import (
	"context"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
)

// DeferOutbox persists transport retry state separately from device attempts.
// A failure from an obsolete batch cannot postpone a newer retry or revive a
// handled row. Database time avoids relying on the Dispatcher's wall clock.
func (s *Store) DeferOutbox(ctx context.Context, item control.OutboxItem, delay time.Duration) (bool, error) {
	if delay < time.Millisecond || delay > 30*time.Second || item.PublicationFailures < 0 {
		return false, control.ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `UPDATE outbox
		SET publication_failures=LEAST(publication_failures,2147483646)+1,
		    next_publish_at=clock_timestamp()+($3::bigint * interval '1 microsecond')
		WHERE outbox_id=$1 AND published_at IS NULL AND publication_failures=$2`,
		item.ID, item.PublicationFailures, delay.Microseconds())
	return tag.RowsAffected() == 1, err
}

type OutboxStats struct {
	Pending       int64
	RetryWaiting  int64
	OldestSeconds float64
}

// OutboxStats includes delayed rows; an empty due-work batch does not mean that
// the backlog is healthy. These are committed-state gauges, not process counters.
func (s *Store) OutboxStats(ctx context.Context) (OutboxStats, error) {
	var stats OutboxStats
	err := s.pool.QueryRow(ctx, `SELECT count(*),
		count(*) FILTER (WHERE next_publish_at > statement_timestamp()),
		COALESCE(GREATEST(EXTRACT(EPOCH FROM statement_timestamp()-min(created_at)),0),0)::double precision
		FROM outbox WHERE published_at IS NULL`).Scan(&stats.Pending, &stats.RetryWaiting, &stats.OldestSeconds)
	return stats, err
}
