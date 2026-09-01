package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &Store{pool: p}, nil
}
func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// TryDispatcherLeadership holds a session advisory lock until release is
// called. check must be called for the lifetime of the Dispatcher: PostgreSQL
// releases session locks when their connection is lost, so continuing after a
// failed check could otherwise create two active leaders.
func (s *Store) TryDispatcherLeadership(ctx context.Context) (release func(), check func(context.Context) error, acquired bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	var ok bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('fleetrelay-dispatcher-v1'))`).Scan(&ok); err != nil {
		conn.Release()
		return nil, nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, nil, false, nil
	}
	release = func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.Exec(releaseCtx, `SELECT pg_advisory_unlock(hashtext('fleetrelay-dispatcher-v1'))`)
		conn.Release()
	}
	check = func(checkCtx context.Context) error {
		var held bool
		err := conn.QueryRow(checkCtx, `SELECT EXISTS(
			SELECT 1 FROM pg_locks
			WHERE locktype='advisory' AND pid=pg_backend_pid() AND granted
		)`).Scan(&held)
		if err != nil {
			return err
		}
		if !held {
			return errors.New("dispatcher advisory leadership lost")
		}
		return nil
	}
	return release, check, true, nil
}

func (s *Store) ListDevices(ctx context.Context) ([]control.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT device_id,display_name,adapter_type,capabilities,COALESCE(last_seen_at,'epoch'::timestamptz) FROM devices ORDER BY device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []control.Device
	for rows.Next() {
		var d control.Device
		var capabilities []byte
		if err = rows.Scan(&d.ID, &d.DisplayName, &d.AdapterType, &capabilities, &d.LastSeenAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(capabilities, &d.Capabilities); err != nil {
			return nil, err
		}
		d.State = "OFFLINE"
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RegisterSession(ctx context.Context, d control.Device, gateway string, now time.Time) (control.DeviceSession, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.DeviceSession{}, err
	}
	defer tx.Rollback(ctx)
	capabilities, _ := json.Marshal(d.Capabilities)
	_, err = tx.Exec(ctx, `INSERT INTO devices(device_id,display_name,adapter_type,capabilities,last_seen_at) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(device_id) DO UPDATE SET display_name=excluded.display_name,adapter_type=excluded.adapter_type,capabilities=excluded.capabilities,last_seen_at=excluded.last_seen_at,version=devices.version+1`, d.ID, d.DisplayName, d.AdapterType, capabilities, now)
	if err != nil {
		return control.DeviceSession{}, err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, d.ID)
	if err != nil {
		return control.DeviceSession{}, err
	}
	var generation uint64
	err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(session_generation),0)+1 FROM device_sessions WHERE device_id=$1`, d.ID).Scan(&generation)
	if err != nil {
		return control.DeviceSession{}, err
	}
	sess := control.DeviceSession{ID: uuid.NewString(), DeviceID: d.ID, GatewayID: gateway, Generation: generation, StartedAt: now}
	_, err = tx.Exec(ctx, `INSERT INTO device_sessions(session_id,device_id,gateway_id,session_generation,started_at) VALUES($1,$2,$3,$4,$5)`, sess.ID, d.ID, gateway, generation, now)
	if err != nil {
		return sess, err
	}
	err = putOutbox(ctx, tx, "device.session.started", d.ID, sess, "session:"+sess.ID, now)
	if err != nil {
		return sess, err
	}
	return sess, tx.Commit(ctx)
}

func (s *Store) AcquireLease(ctx context.Context, deviceID, operator string, ttl time.Duration, now time.Time) (control.Lease, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.Lease{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, deviceID)
	if err != nil {
		return control.Lease{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE device_id=$1)`, deviceID).Scan(&exists); err != nil {
		return control.Lease{}, err
	}
	if !exists {
		return control.Lease{}, control.ErrNotFound
	}
	_, err = tx.Exec(ctx, `UPDATE control_leases SET status='EXPIRED' WHERE device_id=$1 AND status='ACTIVE' AND expires_at <= $2`, deviceID, now)
	if err != nil {
		return control.Lease{}, err
	}
	var l control.Lease
	err = tx.QueryRow(ctx, `SELECT lease_id,device_id,operator_id,lease_epoch,expires_at,status FROM control_leases WHERE device_id=$1 AND status='ACTIVE'`, deviceID).Scan(&l.ID, &l.DeviceID, &l.OperatorID, &l.Epoch, &l.ExpiresAt, &l.Status)
	if err == nil {
		if l.OperatorID != operator {
			return control.Lease{}, control.ErrConflict
		}
		return l, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return control.Lease{}, err
	}
	var epoch uint64
	err = tx.QueryRow(ctx, `INSERT INTO device_counters(device_id,next_sequence,next_lease_epoch) VALUES($1,1,2) ON CONFLICT(device_id) DO UPDATE SET next_lease_epoch=device_counters.next_lease_epoch+1 RETURNING next_lease_epoch-1`, deviceID).Scan(&epoch)
	if err != nil {
		return l, mapFK(err)
	}
	l = control.Lease{ID: uuid.NewString(), DeviceID: deviceID, OperatorID: operator, Epoch: epoch, ExpiresAt: now.Add(ttl), Status: "ACTIVE"}
	_, err = tx.Exec(ctx, `INSERT INTO control_leases(lease_id,device_id,operator_id,lease_epoch,expires_at,status) VALUES($1,$2,$3,$4,$5,'ACTIVE')`, l.ID, l.DeviceID, l.OperatorID, l.Epoch, l.ExpiresAt)
	if err != nil {
		return l, err
	}
	if err = putOutbox(ctx, tx, "lease.changed", deviceID, l, "lease:"+l.ID+":active", now); err != nil {
		return l, err
	}
	return l, tx.Commit(ctx)
}

func (s *Store) RenewLease(ctx context.Context, id, operator string, ttl time.Duration, now time.Time) (control.Lease, error) {
	var l control.Lease
	err := s.pool.QueryRow(ctx, `UPDATE control_leases SET expires_at=$3 WHERE lease_id=$1 AND operator_id=$2 AND status='ACTIVE' AND expires_at>$4 RETURNING lease_id,device_id,operator_id,lease_epoch,expires_at,status`, id, operator, now.Add(ttl), now).Scan(&l.ID, &l.DeviceID, &l.OperatorID, &l.Epoch, &l.ExpiresAt, &l.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, control.ErrStaleLease
	}
	return l, err
}
func (s *Store) ReleaseLease(ctx context.Context, id, operator string, now time.Time) (control.Lease, error) {
	var l control.Lease
	err := s.pool.QueryRow(ctx, `UPDATE control_leases SET status='RELEASED',expires_at=$3 WHERE lease_id=$1 AND operator_id=$2 AND status='ACTIVE' RETURNING lease_id,device_id,operator_id,lease_epoch,expires_at,status`, id, operator, now).Scan(&l.ID, &l.DeviceID, &l.OperatorID, &l.Epoch, &l.ExpiresAt, &l.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, control.ErrStaleLease
	}
	return l, err
}

func (s *Store) CreateCommand(ctx context.Context, r control.CreateCommandRequest, now time.Time) (control.Command, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.Command{}, false, err
	}
	defer tx.Rollback(ctx)
	if c, e := getByIdem(ctx, tx, r.OperatorID, r.DeviceID, r.IdempotencyKey); e == nil {
		return c, true, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return c, false, e
	}
	var epoch uint64
	var expires time.Time
	var status string
	err = tx.QueryRow(ctx, `SELECT lease_epoch,expires_at,status FROM control_leases WHERE lease_id=$1 AND device_id=$2 AND operator_id=$3 FOR UPDATE`, r.LeaseID, r.DeviceID, r.OperatorID).Scan(&epoch, &expires, &status)
	if err != nil || status != "ACTIVE" || !expires.After(now) {
		return control.Command{}, false, control.ErrStaleLease
	}
	var capable bool
	err = tx.QueryRow(ctx, `SELECT capabilities ? $2 FROM devices WHERE device_id=$1`, r.DeviceID, r.Type).Scan(&capable)
	if err != nil {
		return control.Command{}, false, mapFK(err)
	}
	if !capable {
		return control.Command{}, false, control.ErrInvalid
	}
	var seq uint64
	err = tx.QueryRow(ctx, `UPDATE device_counters SET next_sequence=next_sequence+1 WHERE device_id=$1 RETURNING next_sequence-1`, r.DeviceID).Scan(&seq)
	if err != nil {
		return control.Command{}, false, err
	}
	c := control.Command{ID: uuid.NewString(), IdempotencyKey: r.IdempotencyKey, OperatorID: r.OperatorID, DeviceID: r.DeviceID, DeviceSequence: seq, LeaseID: r.LeaseID, LeaseEpoch: epoch, Type: r.Type, Payload: r.Payload, DeliveryPolicy: r.DeliveryPolicy, ExpiresAt: r.ExpiresAt, Status: control.StatusPendingTransport, CreatedAt: now, UpdatedAt: now}
	_, err = tx.Exec(ctx, `INSERT INTO commands(command_id,operator_id,device_id,idempotency_key,device_sequence,lease_id,lease_epoch,command_type,payload,delivery_policy,expires_at,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)`, c.ID, c.OperatorID, c.DeviceID, c.IdempotencyKey, c.DeviceSequence, c.LeaseID, c.LeaseEpoch, c.Type, c.Payload, c.DeliveryPolicy, c.ExpiresAt, c.Status, now)
	if err != nil {
		return c, false, err
	}
	if err = putOutbox(ctx, tx, "command.created", c.ID, c, "command:"+c.ID+":created", now); err != nil {
		return c, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_records(actor_id,action,target_id,result) VALUES($1,'command.create',$2,'ACCEPTED')`, c.OperatorID, c.ID)
	if err != nil {
		return c, false, err
	}
	return c, false, tx.Commit(ctx)
}

func scanCommand(row pgx.Row) (control.Command, error) {
	var c control.Command
	err := row.Scan(&c.ID, &c.IdempotencyKey, &c.OperatorID, &c.DeviceID, &c.DeviceSequence, &c.LeaseID, &c.LeaseEpoch, &c.Type, &c.Payload, &c.DeliveryPolicy, &c.ExpiresAt, &c.Status, &c.ResultDetail, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

const cols = `command_id,idempotency_key,operator_id,device_id,device_sequence,lease_id,lease_epoch,command_type,payload,delivery_policy,expires_at,status,result_detail,created_at,updated_at`
const retryCols = `c.command_id,c.idempotency_key,c.operator_id,c.device_id,c.device_sequence,c.lease_id,c.lease_epoch,c.command_type,c.payload,c.delivery_policy,c.expires_at,c.status,c.result_detail,c.created_at,c.updated_at`

func getByIdem(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, op, dev, key string) (control.Command, error) {
	return scanCommand(q.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE operator_id=$1 AND device_id=$2 AND idempotency_key=$3`, op, dev, key))
}
func (s *Store) GetCommand(ctx context.Context, id string) (control.Command, error) {
	c, e := scanCommand(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE command_id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		e = control.ErrNotFound
	}
	return c, e
}
func (s *Store) TransitionCommand(ctx context.Context, id string, to control.CommandStatus, detail string, now time.Time) (control.Command, bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return control.Command{}, false, e
	}
	defer tx.Rollback(ctx)
	c, e := scanCommand(tx.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE command_id=$1 FOR UPDATE`, id))
	if e != nil {
		return c, false, e
	}
	if c.Status == to {
		return c, true, nil
	}
	if e = control.ValidateTransition(c.Status, to); e != nil {
		return c, false, e
	}
	if to == control.StatusDispatched && !c.ExpiresAt.After(now) {
		return c, false, control.ErrExpired
	}
	c.Status = to
	c.ResultDetail = detail
	c.UpdatedAt = now
	_, e = tx.Exec(ctx, `UPDATE commands SET status=$2,result_detail=$3,updated_at=$4 WHERE command_id=$1`, id, to, detail, now)
	if e != nil {
		return c, false, e
	}
	e = putOutbox(ctx, tx, "command.status", id, c, "command:"+id+":"+string(to), now)
	if e != nil {
		return c, false, e
	}
	return c, false, tx.Commit(ctx)
}
func (s *Store) MarkDispatched(ctx context.Context, receipt control.DispatchReceipt, now time.Time) (control.Command, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.Command{}, false, err
	}
	defer tx.Rollback(ctx)
	c, err := scanCommand(tx.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE command_id=$1 FOR UPDATE`, receipt.CommandID))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, control.ErrNotFound
	}
	if err != nil {
		return c, false, err
	}
	if c.DeviceID != receipt.DeviceID {
		return c, false, control.ErrStaleSession
	}
	// Every command mutation locks the command row before the per-device
	// advisory lock. Keeping one global order prevents retry/ACK deadlocks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, c.DeviceID); err != nil {
		return control.Command{}, false, err
	}
	if c.Status == control.StatusDispatched || c.Status == control.StatusSucceeded || c.Status == control.StatusFailed {
		return c, true, nil
	}
	if c.Status != control.StatusPendingTransport {
		return c, false, control.ErrInvalidState
	}
	if !c.ExpiresAt.After(now) {
		return c, false, control.ErrExpired
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM command_attempts a
		WHERE a.command_id=$1 AND a.target_gateway_id=$2 AND a.target_session_generation=$3
		AND a.attempt=(SELECT MAX(attempt) FROM command_attempts WHERE command_id=$1)
	) AND EXISTS(
		SELECT 1 FROM device_sessions s WHERE s.device_id=$4 AND s.gateway_id=$2 AND s.session_generation=$3
		AND s.session_generation=(SELECT MAX(session_generation) FROM device_sessions WHERE device_id=$4)
	)`, c.ID, receipt.GatewayID, receipt.SessionGeneration, c.DeviceID).Scan(&valid)
	if err != nil {
		return c, false, err
	}
	if !valid {
		return c, false, control.ErrStaleSession
	}
	c.Status = control.StatusDispatched
	c.UpdatedAt = now
	if _, err = tx.Exec(ctx, `UPDATE commands SET status=$2,updated_at=$3 WHERE command_id=$1`, c.ID, c.Status, now); err != nil {
		return c, false, err
	}
	if err = putOutbox(ctx, tx, "command.status", c.ID, c, "command:"+c.ID+":"+string(c.Status), now); err != nil {
		return c, false, err
	}
	return c, false, tx.Commit(ctx)
}

func (s *Store) Acknowledge(ctx context.Context, ack control.Ack, now time.Time) (control.Command, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.Command{}, false, err
	}
	defer tx.Rollback(ctx)
	c, err := scanCommand(tx.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE command_id=$1 FOR UPDATE`, ack.CommandID))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, control.ErrNotFound
	}
	if err != nil {
		return c, false, err
	}
	if c.DeviceID != ack.DeviceID || c.LeaseEpoch != ack.LeaseEpoch || c.DeviceSequence != ack.DeviceSequence {
		return c, false, control.ErrInvalid
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, c.DeviceID); err != nil {
		return control.Command{}, false, err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM command_attempts a WHERE a.command_id=$1 AND a.target_gateway_id=$2 AND a.target_session_generation=$3
		AND a.attempt=(SELECT MAX(attempt) FROM command_attempts WHERE command_id=$1)
	) AND EXISTS(
		SELECT 1 FROM device_sessions s WHERE s.device_id=$4 AND s.gateway_id=$2 AND s.session_generation=$3
		AND s.session_generation=(SELECT MAX(session_generation) FROM device_sessions WHERE device_id=$4)
	)`, c.ID, ack.GatewayID, ack.SessionGeneration, c.DeviceID).Scan(&valid)
	if err != nil {
		return c, false, err
	}
	if !valid {
		return c, false, control.ErrStaleSession
	}
	if c.Status == ack.Result {
		return c, true, nil
	}
	if c.Status == control.StatusSucceeded || c.Status == control.StatusFailed {
		return c, false, control.ErrAckConflict
	}
	if c.Status == control.StatusPendingTransport {
		if !c.ExpiresAt.After(now) {
			return c, false, control.ErrExpired
		}
	} else if err = control.ValidateTransition(c.Status, ack.Result); err != nil {
		return c, false, err
	}
	c.Status = ack.Result
	c.ResultDetail = ack.Detail
	c.UpdatedAt = now
	if _, err = tx.Exec(ctx, `UPDATE commands SET status=$2,result_detail=$3,updated_at=$4 WHERE command_id=$1`, c.ID, c.Status, c.ResultDetail, now); err != nil {
		return c, false, err
	}
	if err = putOutbox(ctx, tx, "command.status", c.ID, c, "command:"+c.ID+":"+string(c.Status), now); err != nil {
		return c, false, err
	}
	return c, false, tx.Commit(ctx)
}

func (s *Store) CreateAttempt(ctx context.Context, id, gateway string, generation uint64, routed, deadline time.Time) (control.CommandAttempt, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return control.CommandAttempt{}, err
	}
	defer tx.Rollback(ctx)
	c, err := scanCommand(tx.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE command_id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return control.CommandAttempt{}, control.ErrNotFound
	}
	if err != nil {
		return control.CommandAttempt{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, c.DeviceID); err != nil {
		return control.CommandAttempt{}, err
	}
	if c.Status != control.StatusPendingTransport && c.Status != control.StatusDispatched {
		return control.CommandAttempt{}, control.ErrInvalidState
	}
	if !c.ExpiresAt.After(routed) {
		return control.CommandAttempt{}, control.ErrExpired
	}
	var leaseValid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM control_leases WHERE lease_id=$1 AND device_id=$2 AND lease_epoch=$3 AND status='ACTIVE' AND expires_at>$4)`, c.LeaseID, c.DeviceID, c.LeaseEpoch, routed).Scan(&leaseValid)
	if err != nil {
		return control.CommandAttempt{}, err
	}
	if !leaseValid {
		return control.CommandAttempt{}, control.ErrStaleLease
	}
	var sessionValid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM device_sessions WHERE device_id=$1 AND gateway_id=$2 AND session_generation=$3 AND session_generation=(SELECT MAX(session_generation) FROM device_sessions WHERE device_id=$1))`, c.DeviceID, gateway, generation).Scan(&sessionValid)
	if err != nil {
		return control.CommandAttempt{}, err
	}
	if !sessionValid {
		return control.CommandAttempt{}, control.ErrStaleSession
	}
	var blocked bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commands WHERE device_id=$1 AND device_sequence<$2 AND status NOT IN ('SUCCEEDED','FAILED','EXPIRED','CANCELLED'))`, c.DeviceID, c.DeviceSequence).Scan(&blocked)
	if err != nil {
		return control.CommandAttempt{}, err
	}
	if blocked {
		return control.CommandAttempt{}, control.ErrConflict
	}
	var n uint32
	err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt),0)+1 FROM command_attempts WHERE command_id=$1`, id).Scan(&n)
	if err != nil {
		return control.CommandAttempt{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO command_attempts(command_id,attempt,target_gateway_id,target_session_generation,routed_at,attempt_deadline) VALUES($1,$2,$3,$4,$5,$6)`, id, n, gateway, generation, routed, deadline)
	if err != nil {
		return control.CommandAttempt{}, err
	}
	a := control.CommandAttempt{CommandID: id, Attempt: n, TargetGatewayID: gateway, TargetSessionGeneration: generation, RoutedAt: routed, Deadline: deadline}
	return a, tx.Commit(ctx)
}

func (s *Store) PrepareOutboxCommand(ctx context.Context, id string, now time.Time) (control.Command, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.Command{}, false, err
	}
	defer tx.Rollback(ctx)
	c, err := scanCommand(tx.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE command_id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, control.ErrNotFound
	}
	if err != nil {
		return c, false, err
	}
	if c.Status != control.StatusPendingTransport {
		return c, true, nil
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, c.DeviceID); err != nil {
		return c, false, err
	}
	to := control.CommandStatus("")
	if !c.ExpiresAt.After(now) {
		to = control.StatusExpired
	} else {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM control_leases WHERE lease_id=$1 AND device_id=$2 AND lease_epoch=$3 AND status='ACTIVE' AND expires_at>$4)`, c.LeaseID, c.DeviceID, c.LeaseEpoch, now).Scan(&valid); err != nil {
			return c, false, err
		}
		if !valid {
			to = control.StatusCancelled
		}
	}
	if to == "" {
		return c, false, nil
	}
	c.Status = to
	c.UpdatedAt = now
	if _, err = tx.Exec(ctx, `UPDATE commands SET status=$2,updated_at=$3 WHERE command_id=$1`, c.ID, to, now); err != nil {
		return c, false, err
	}
	if err = putOutbox(ctx, tx, "command.status", c.ID, c, "command:"+c.ID+":"+string(to), now); err != nil {
		return c, false, err
	}
	return c, true, tx.Commit(ctx)
}

func (s *Store) ListRetryCandidates(ctx context.Context, now time.Time, limit int) ([]control.RetryCandidate, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+retryCols+`,a.attempt,a.target_gateway_id,a.target_session_generation,a.routed_at,a.attempt_deadline,(SELECT COUNT(*) FROM command_attempts x WHERE x.command_id=c.command_id)
		FROM commands c JOIN LATERAL (SELECT * FROM command_attempts WHERE command_id=c.command_id ORDER BY attempt DESC LIMIT 1) a ON true
		WHERE c.status IN ('PENDING_TRANSPORT','DISPATCHED') AND a.attempt_deadline<=$1 ORDER BY a.attempt_deadline LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []control.RetryCandidate
	for rows.Next() {
		var r control.RetryCandidate
		if err = rows.Scan(&r.Command.ID, &r.Command.IdempotencyKey, &r.Command.OperatorID, &r.Command.DeviceID, &r.Command.DeviceSequence, &r.Command.LeaseID, &r.Command.LeaseEpoch, &r.Command.Type, &r.Command.Payload, &r.Command.DeliveryPolicy, &r.Command.ExpiresAt, &r.Command.Status, &r.Command.ResultDetail, &r.Command.CreatedAt, &r.Command.UpdatedAt, &r.LastAttempt.Attempt, &r.LastAttempt.TargetGatewayID, &r.LastAttempt.TargetSessionGeneration, &r.LastAttempt.RoutedAt, &r.LastAttempt.Deadline, &r.AttemptCount); err != nil {
			return nil, err
		}
		r.LastAttempt.CommandID = r.Command.ID
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ResolveAttemptDeadline(ctx context.Context, id string, max uint32, now time.Time) (control.Command, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.Command{}, false, err
	}
	defer tx.Rollback(ctx)
	c, err := scanCommand(tx.QueryRow(ctx, `SELECT `+cols+` FROM commands WHERE command_id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, control.ErrNotFound
	}
	if err != nil {
		return c, false, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, c.DeviceID); err != nil {
		return c, false, err
	}
	var attempts uint32
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM command_attempts WHERE command_id=$1`, id).Scan(&attempts); err != nil {
		return c, false, err
	}
	to := control.CommandStatus("")
	if c.Status == control.StatusPendingTransport && !c.ExpiresAt.After(now) {
		to = control.StatusExpired
	}
	if c.Status == control.StatusDispatched && (!c.ExpiresAt.After(now) || attempts >= max) {
		to = control.StatusAckTimeout
	}
	if to == "" {
		return c, false, nil
	}
	if err = control.ValidateTransition(c.Status, to); err != nil {
		return c, false, err
	}
	c.Status = to
	c.UpdatedAt = now
	if _, err = tx.Exec(ctx, `UPDATE commands SET status=$2,updated_at=$3 WHERE command_id=$1`, id, to, now); err != nil {
		return c, false, err
	}
	if err = putOutbox(ctx, tx, "command.status", id, c, "command:"+id+":"+string(to), now); err != nil {
		return c, false, err
	}
	return c, true, tx.Commit(ctx)
}
func (s *Store) ClaimOutbox(ctx context.Context, limit int) ([]control.OutboxItem, error) {
	rows, e := s.pool.Query(ctx, `SELECT outbox_id,aggregate_id,kind,payload,created_at FROM outbox WHERE published_at IS NULL ORDER BY created_at LIMIT $1`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []control.OutboxItem
	for rows.Next() {
		var i control.OutboxItem
		if e = rows.Scan(&i.ID, &i.AggregateID, &i.Kind, &i.Payload, &i.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (s *Store) MarkOutboxPublished(ctx context.Context, id string, now time.Time) error {
	tag, e := s.pool.Exec(ctx, `UPDATE outbox SET published_at=$2 WHERE outbox_id=$1 AND published_at IS NULL`, id, now)
	if e == nil && tag.RowsAffected() == 0 {
		return control.ErrNotFound
	}
	return e
}
func putOutbox(ctx context.Context, tx pgx.Tx, kind, aggregate string, value any, key string, now time.Time) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO outbox(aggregate_id,kind,payload,business_key,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(business_key) DO NOTHING`, aggregate, kind, b, key, now)
	return e
}
func mapFK(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return control.ErrNotFound
	}
	return fmt.Errorf("postgres: %w", err)
}
