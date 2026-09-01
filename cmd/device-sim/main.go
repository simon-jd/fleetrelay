package main

import (
	"context"
	"fmt"
	fleetrelayv1 "github.com/fleetrelay/fleetrelay/gen/fleetrelay/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	addresses := strings.Split(env("GATEWAY_GRPC_ADDR", "stream-gateway:9090"), ",")
	device := env("DEVICE_ID", "vacuum-floor-2")
	state := newDeviceState()
	backoff := time.Second
	next := 0
	for ctx.Err() == nil {
		addr := strings.TrimSpace(addresses[next%len(addresses)])
		next++
		conn, e := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e == nil {
			e = run(ctx, conn, device, state, log)
			conn.Close()
		}
		log.Warn("device disconnected", "error", e, "retry", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

type cachedResult struct {
	Result fleetrelayv1.AckResult
	Detail string
}
type deviceState struct {
	mu                  sync.Mutex
	executed            map[string]cachedResult
	dropped             map[string]bool
	lastSeq, leaseEpoch uint64
}

func newDeviceState() *deviceState {
	return &deviceState{executed: make(map[string]cachedResult), dropped: make(map[string]bool)}
}
func run(ctx context.Context, conn *grpc.ClientConn, device string, state *deviceState, log *slog.Logger) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, e := fleetrelayv1.NewDeviceRelayServiceClient(conn).Connect(runCtx)
	if e != nil {
		return e
	}
	e = stream.Send(&fleetrelayv1.ConnectRequest{Message: &fleetrelayv1.ConnectRequest_Hello{Hello: &fleetrelayv1.DeviceHello{DeviceId: device, Adapter: "vacuum-sim", ClientVersion: "1.0.0", Capabilities: []string{"START_TASK", "PAUSE_TASK", "RESUME_TASK", "RETURN_HOME", "CANCEL_TASK", "QUERY_STATE"}}}})
	if e != nil {
		return e
	}
	first, e := stream.Recv()
	if e != nil {
		return e
	}
	hello := first.GetHello()
	if hello == nil {
		return fmt.Errorf("missing server hello")
	}
	generation := hello.SessionGeneration
	var sendMu sync.Mutex
	send := func(m *fleetrelayv1.ConnectRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(m)
	}
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		battery := float64(87)
		for {
			select {
			case <-runCtx.Done():
				return
			case now := <-tick.C:
				if send(&fleetrelayv1.ConnectRequest{Message: &fleetrelayv1.ConnectRequest_Heartbeat{Heartbeat: &fleetrelayv1.Heartbeat{SessionGeneration: generation}}}) != nil {
					cancel()
					return
				}
				fields, _ := structpb.NewStruct(map[string]any{"battery_percent": battery, "state": "IDLE"})
				if send(&fleetrelayv1.ConnectRequest{Message: &fleetrelayv1.ConnectRequest_Telemetry{Telemetry: &fleetrelayv1.Telemetry{Topic: "state", Fields: fields, ObservedAt: timestamppb.New(now)}}}) != nil {
					cancel()
					return
				}
				battery -= 0.1
			}
		}
	}()
	for {
		m, e := stream.Recv()
		if e != nil {
			return e
		}
		c := m.GetCommand()
		if c == nil {
			continue
		}
		state.mu.Lock()
		cached, seen := state.executed[c.CommandId]
		ack := &fleetrelayv1.CommandAck{CommandId: c.CommandId, DeviceId: device, SessionGeneration: generation, LeaseEpoch: c.LeaseEpoch, DeviceSequence: c.DeviceSequence}
		if !seen {
			ack.Result = fleetrelayv1.AckResult_ACK_RESULT_SUCCEEDED
			ack.Detail = "executed by deterministic simulator"
			switch {
			case c.SessionGeneration != generation:
				ack.Result = fleetrelayv1.AckResult_ACK_RESULT_REJECTED
				ack.Detail = "stale_session"
			case c.LeaseEpoch < state.leaseEpoch:
				ack.Result = fleetrelayv1.AckResult_ACK_RESULT_REJECTED
				ack.Detail = "stale_lease"
			case c.DeviceSequence <= state.lastSeq:
				ack.Result = fleetrelayv1.AckResult_ACK_RESULT_REJECTED
				ack.Detail = "stale_sequence"
			case c.ExpiresAt.AsTime().Before(time.Now()):
				ack.Result = fleetrelayv1.AckResult_ACK_RESULT_REJECTED
				ack.Detail = "expired"
			default:
				time.Sleep(duration("ADAPTER_DELAY_MS", 25) * time.Millisecond)
				state.lastSeq = c.DeviceSequence
				state.leaseEpoch = c.LeaseEpoch
			}
			state.executed[c.CommandId] = cachedResult{Result: ack.Result, Detail: ack.Detail}
		} else {
			ack.Result = cached.Result
			ack.Detail = cached.Detail
		}
		drop := env("DROP_FIRST_ACK", "false") == "true" && !state.dropped[c.CommandId]
		if drop {
			state.dropped[c.CommandId] = true
		}
		state.mu.Unlock()
		if drop {
			continue
		}
		if e = send(&fleetrelayv1.ConnectRequest{Message: &fleetrelayv1.ConnectRequest_CommandAck{CommandAck: ack}}); e != nil {
			return e
		}
		log.Info("command acknowledged", "command_id", c.CommandId, "result", ack.Result.String())
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func duration(k string, d int) time.Duration {
	v, e := strconv.Atoi(env(k, strconv.Itoa(d)))
	if e != nil {
		return time.Duration(d)
	}
	return time.Duration(v)
}
