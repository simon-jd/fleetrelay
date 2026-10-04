package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	fleetrelayv1 "github.com/fleetrelay/fleetrelay/gen/fleetrelay/v1"
	"google.golang.org/protobuf/proto"
)

const maxJournalEntries = 10000

type cachedResult struct {
	Result      fleetrelayv1.AckResult `json:"result"`
	Detail      string                 `json:"detail"`
	Fingerprint string                 `json:"fingerprint"`
	Sequence    uint64                 `json:"sequence"`
	LeaseEpoch  uint64                 `json:"lease_epoch"`
}

type journalSnapshot struct {
	Version    int                     `json:"version"`
	DeviceID   string                  `json:"device_id"`
	LastSeq    uint64                  `json:"last_sequence"`
	LeaseEpoch uint64                  `json:"lease_epoch"`
	Effects    uint64                  `json:"simulated_effects"`
	Executed   map[string]cachedResult `json:"executed"`
}

type deviceState struct {
	mu       sync.Mutex
	snapshot journalSnapshot
	dropped  map[string]bool
	path     string
	lock     *os.File
	failed   bool
}

func newDeviceState() *deviceState {
	return &deviceState{snapshot: journalSnapshot{Version: 1, Executed: make(map[string]cachedResult)}, dropped: make(map[string]bool)}
}

func openDeviceState(path, device string) (*deviceState, error) {
	s := newDeviceState()
	s.path, s.snapshot.DeviceID = path, device
	if path == "" {
		return s, nil
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("journal already owned: %w", err)
	}
	s.lock = lock
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 16<<20))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&s.snapshot)
	if err == nil {
		var trailing any
		if e := decoder.Decode(&trailing); !errors.Is(e, io.EOF) {
			err = errors.New("journal has trailing data")
		}
	}
	if err == nil {
		err = validateSnapshot(s.snapshot, device)
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("invalid journal; refusing execution: %w", err)
	}
	return s, nil
}

func validateSnapshot(s journalSnapshot, device string) error {
	if s.Version != 1 || s.DeviceID != device || s.Executed == nil || len(s.Executed) > maxJournalEntries || s.Effects != uint64(len(s.Executed)) {
		return errors.New("journal header/count mismatch")
	}
	var maxSeq, maxEpoch uint64
	for id, r := range s.Executed {
		hash, err := hex.DecodeString(r.Fingerprint)
		if id == "" || err != nil || len(hash) != sha256.Size || r.Sequence == 0 || r.LeaseEpoch == 0 || r.Result != fleetrelayv1.AckResult_ACK_RESULT_SUCCEEDED {
			return errors.New("invalid cached result")
		}
		maxSeq, maxEpoch = max(maxSeq, r.Sequence), max(maxEpoch, r.LeaseEpoch)
	}
	if maxSeq != s.LastSeq || maxEpoch != s.LeaseEpoch {
		return errors.New("journal fence mismatch")
	}
	return nil
}

func (s *deviceState) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close() // Closing also releases the process-scoped flock.
	s.lock = nil
	return err
}

func commandFingerprint(c *fleetrelayv1.Command) (string, error) {
	stable := proto.Clone(c).(*fleetrelayv1.Command)
	// Rerouting to a replacement session must not change command identity.
	stable.SessionGeneration = 0
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(stable)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// execute models the side effect as a durable counter in the same snapshot as
// its cached result. That atomicity is specific to this simulator: it cannot
// establish atomicity with a physical actuator or external vendor API.
func (s *deviceState) execute(ctx context.Context, device string, generation uint64, c *fleetrelayv1.Command, delay time.Duration) (*fleetrelayv1.CommandAck, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return nil, false, errors.New("journal persistence failed; restart required")
	}
	ack := &fleetrelayv1.CommandAck{CommandId: c.CommandId, DeviceId: device, SessionGeneration: generation, LeaseEpoch: c.LeaseEpoch, DeviceSequence: c.DeviceSequence}
	reject := func(detail string) (*fleetrelayv1.CommandAck, bool, error) {
		ack.Result, ack.Detail = fleetrelayv1.AckResult_ACK_RESULT_REJECTED, detail
		return ack, false, nil
	}
	if c.CommandId == "" || c.DeviceSequence == 0 || c.LeaseEpoch == 0 || c.ExpiresAt == nil || c.ExpiresAt.CheckValid() != nil {
		return reject("invalid_command")
	}
	if c.SessionGeneration != generation {
		return reject("stale_session")
	}
	fingerprint, err := commandFingerprint(c)
	if err != nil {
		return nil, false, err
	}
	if cached, ok := s.snapshot.Executed[c.CommandId]; ok {
		if cached.Fingerprint != fingerprint {
			return reject("command_fingerprint_conflict")
		}
		ack.Result, ack.Detail = cached.Result, cached.Detail
		return ack, false, nil
	}
	if c.LeaseEpoch < s.snapshot.LeaseEpoch {
		return reject("stale_lease")
	}
	if c.DeviceSequence <= s.snapshot.LastSeq {
		return reject("stale_sequence")
	}
	if !c.ExpiresAt.AsTime().After(time.Now()) {
		return reject("expired")
	}
	if len(s.snapshot.Executed) >= maxJournalEntries {
		return nil, false, errors.New("simulator journal capacity reached")
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-timer.C:
	}
	ack.Result, ack.Detail = fleetrelayv1.AckResult_ACK_RESULT_SUCCEEDED, "executed by deterministic simulator"
	next := s.snapshot
	next.LastSeq, next.LeaseEpoch, next.Effects = c.DeviceSequence, c.LeaseEpoch, next.Effects+1
	next.Executed = make(map[string]cachedResult, len(s.snapshot.Executed)+1)
	for id, result := range s.snapshot.Executed {
		next.Executed[id] = result
	}
	next.Executed[c.CommandId] = cachedResult{Result: ack.Result, Detail: ack.Detail, Fingerprint: fingerprint, Sequence: c.DeviceSequence, LeaseEpoch: c.LeaseEpoch}
	if s.path != "" {
		if err := persistSnapshot(s.path, next); err != nil {
			// Rename may have succeeded before a later sync failed. Never execute
			// again from stale in-memory state after an ambiguous disk outcome.
			s.failed = true
			return nil, false, err
		}
	}
	s.snapshot = next
	return ack, true, nil
}

func persistSnapshot(path string, snapshot journalSnapshot) error {
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".fleetrelay-journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
