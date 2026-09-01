package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	fleetrelayv1 "github.com/fleetrelay/fleetrelay/gen/fleetrelay/v1"
	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/relay"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const deliveryGroup = "fleetrelay-delivery"

var refreshPresence = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
if not current then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2], 'NX')
  return 1
end
return -1`)
var deletePresence = redis.NewScript(`if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) else return 0 end`)

type gateway struct {
	fleetrelayv1.UnimplementedDeviceRelayServiceServer
	id, controlURL, internalToken, devToken, viewerToken string
	instanceValue                                        string
	rdb                                                  *redis.Client
	registry                                             *relay.Registry
	log                                                  *slog.Logger
}
type presence struct {
	GatewayID       string `json:"gateway_id"`
	GatewayInstance string `json:"gateway_instance"`
	SessionID       string `json:"session_id"`
	Generation      uint64 `json:"generation"`
}

func (g *gateway) streamName() string { return "relay:gateway:" + g.id + ":commands" }

func (g *gateway) startLiveness(ctx context.Context, cancel context.CancelFunc) error {
	key := "relay:gateway:" + g.id + ":alive"
	claimed, err := g.rdb.SetNX(ctx, key, g.instanceValue, 3*time.Second).Result()
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("gateway id %q is already active", g.id)
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer deletePresence.Run(context.Background(), g.rdb, []string{key}, g.instanceValue)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result, err := refreshPresence.Run(ctx, g.rdb, []string{key}, g.instanceValue, 3000).Int()
				if err != nil && ctx.Err() == nil {
					g.log.Warn("refresh gateway liveness", "error", err)
				} else if result < 0 {
					g.log.Error("gateway liveness ownership lost; stopping", "gateway_id", g.id)
					cancel()
					return
				}
			}
		}
	}()
	return nil
}

func (g *gateway) Connect(stream grpc.BidiStreamingServer[fleetrelayv1.ConnectRequest, fleetrelayv1.ConnectResponse]) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil || hello.DeviceId == "" {
		return status.Error(codes.InvalidArgument, "first message must be DeviceHello")
	}
	d := control.Device{ID: hello.DeviceId, DisplayName: hello.DeviceId, AdapterType: hello.Adapter, Capabilities: hello.Capabilities}
	var sess control.DeviceSession
	if err = g.post(ctx, "/v1/sessions", map[string]any{"device": d, "gateway_id": g.id}, &sess, nil); err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	p := presence{GatewayID: g.id, GatewayInstance: g.instanceValue, SessionID: sess.ID, Generation: sess.Generation}
	raw, _ := json.Marshal(p)
	key := "relay:presence:" + hello.DeviceId
	if err = g.rdb.Set(ctx, key, raw, 20*time.Second).Err(); err != nil {
		return status.Error(codes.Unavailable, "redis presence unavailable")
	}
	conn := &relay.DeviceConnection{Generation: sess.Generation, Commands: make(chan relay.RoutedCommand, 16), Replaced: make(chan struct{})}
	g.registry.Register(hello.DeviceId, conn)
	defer func() {
		g.registry.Remove(hello.DeviceId, sess.Generation)
		deletePresence.Run(context.Background(), g.rdb, []string{key}, string(raw))
	}()
	if err = stream.Send(&fleetrelayv1.ConnectResponse{Message: &fleetrelayv1.ConnectResponse_Hello{Hello: &fleetrelayv1.ServerHello{SessionId: sess.ID, SessionGeneration: sess.Generation, HeartbeatIntervalSeconds: 5, ServerTime: timestamppb.Now()}}}); err != nil {
		return err
	}
	recvErr := make(chan error, 1)
	go func() {
		for {
			m, e := stream.Recv()
			if e != nil {
				recvErr <- e
				return
			}
			switch x := m.Message.(type) {
			case *fleetrelayv1.ConnectRequest_Heartbeat:
				if x.Heartbeat.SessionGeneration == sess.Generation {
					refreshPresence.Run(ctx, g.rdb, []string{key}, string(raw), 20000)
				}
			case *fleetrelayv1.ConnectRequest_CommandAck:
				a := x.CommandAck
				if a.DeviceId != hello.DeviceId {
					recvErr <- fmt.Errorf("ACK device mismatch")
					return
				}
				result := control.StatusFailed
				if a.Result == fleetrelayv1.AckResult_ACK_RESULT_SUCCEEDED {
					result = control.StatusSucceeded
				}
				ack := map[string]any{"device_id": a.DeviceId, "gateway_id": g.id, "session_generation": a.SessionGeneration, "lease_epoch": a.LeaseEpoch, "device_sequence": a.DeviceSequence, "result": result, "detail": a.Detail}
				ackCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				e = g.post(ackCtx, "/v1/commands/"+a.CommandId+"/ack", ack, nil, nil)
				cancel()
				if e != nil {
					recvErr <- fmt.Errorf("persist ACK: %w", e)
					return
				}
			case *fleetrelayv1.ConnectRequest_Telemetry:
				g.rdb.XAdd(ctx, &redis.XAddArgs{Stream: "relay:telemetry", MaxLen: 10000, Approx: true, Values: map[string]any{"device_id": hello.DeviceId, "topic": x.Telemetry.Topic, "payload": x.Telemetry.Fields.String()}})
			case *fleetrelayv1.ConnectRequest_Alert:
				g.rdb.XAdd(ctx, &redis.XAddArgs{Stream: "relay:fleet:events", MaxLen: 10000, Approx: true, Values: map[string]any{"kind": "device.alert", "device_id": hello.DeviceId, "code": x.Alert.Code, "detail": x.Alert.Detail}})
			}
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.Replaced:
			return status.Error(codes.Aborted, "superseded by newer session generation")
		case err = <-recvErr:
			return err
		case routed := <-conn.Commands:
			c := routed.Command
			if err = stream.Send(&fleetrelayv1.ConnectResponse{Message: &fleetrelayv1.ConnectResponse_Command{Command: c}}); err != nil {
				return err
			}
			markCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = g.post(markCtx, "/v1/commands/"+c.CommandId+"/dispatched", map[string]any{"device_id": hello.DeviceId, "gateway_id": g.id, "session_generation": sess.Generation}, nil, nil)
			cancel()
			if err != nil {
				return fmt.Errorf("persist DISPATCHED: %w", err)
			}
			if err = g.ackDelivery(ctx, routed.RedisMessageID); err != nil {
				return fmt.Errorf("ack Redis delivery: %w", err)
			}
		case <-ticker.C:
			if err = stream.Send(&fleetrelayv1.ConnectResponse{Message: &fleetrelayv1.ConnectResponse_Heartbeat{Heartbeat: &fleetrelayv1.ServerHeartbeat{ServerTime: timestamppb.Now()}}}); err != nil {
				return err
			}
		}
	}
}

func (g *gateway) ackDelivery(ctx context.Context, messageID string) error {
	_, err := g.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.XAck(ctx, g.streamName(), deliveryGroup, messageID)
		pipe.XDel(ctx, g.streamName(), messageID)
		return nil
	})
	return err
}

func (g *gateway) post(ctx context.Context, path string, in, out any, headers http.Header) error {
	b, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.controlURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-FleetRelay-Internal-Token", g.internalToken)
	for k, v := range headers {
		req.Header[k] = v
	}
	return g.do(req, out)
}
func (g *gateway) get(ctx context.Context, path string, out any, headers http.Header) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, g.controlURL+path, nil)
	for k, v := range headers {
		req.Header[k] = v
	}
	return g.do(req, out)
}
func (g *gateway) do(req *http.Request, out any) error {
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("control-api %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (g *gateway) consume(ctx context.Context) {
	stream := g.streamName()
	if e := g.rdb.XGroupCreateMkStream(ctx, stream, deliveryGroup, "0").Err(); e != nil && !strings.Contains(e.Error(), "BUSYGROUP") {
		g.log.Error("create delivery consumer group", "error", e)
		return
	}
	for ctx.Err() == nil {
		rows, e := g.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: deliveryGroup, Consumer: g.id, Streams: []string{stream, ">"}, Count: 32, Block: 2 * time.Second}).Result()
		if e != nil && !errors.Is(e, redis.Nil) {
			g.log.Warn("read delivery group", "error", e)
		}
		for _, row := range rows {
			for _, m := range row.Messages {
				g.routeMessage(ctx, m)
			}
		}
		claimed, _, e := g.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: stream, Group: deliveryGroup, Consumer: g.id, MinIdle: 3 * time.Second, Start: "0-0", Count: 32}).Result()
		if e == nil {
			for _, m := range claimed {
				g.routeMessage(ctx, m)
			}
		}
	}
}
func (g *gateway) routeMessage(ctx context.Context, m redis.XMessage) {
	raw := fmt.Sprint(m.Values["command"])
	var c control.Command
	if json.Unmarshal([]byte(raw), &c) != nil {
		g.log.Error("discard malformed delivery", "message_id", m.ID)
		if err := g.ackDelivery(ctx, m.ID); err != nil {
			g.log.Warn("discard malformed delivery", "message_id", m.ID, "error", err)
		}
		return
	}
	var generation uint64
	fmt.Sscan(fmt.Sprint(m.Values["generation"]), &generation)
	payload := map[string]any{}
	json.Unmarshal(c.Payload, &payload)
	st, _ := structpb.NewStruct(payload)
	pc := &fleetrelayv1.Command{CommandId: c.ID, IdempotencyKey: c.IdempotencyKey, CommandType: c.Type, Payload: st, DeviceSequence: c.DeviceSequence, SessionGeneration: generation, LeaseEpoch: c.LeaseEpoch, ExpiresAt: timestamppb.New(c.ExpiresAt)}
	if e := g.registry.Route(c.DeviceID, generation, relay.RoutedCommand{Command: pc, RedisMessageID: m.ID}); e != nil {
		g.log.Debug("delivery remains pending", "device_id", c.DeviceID, "error", e)
		// Cross-Gateway retry can make this private-stream entry obsolete. Once
		// PostgreSQL says the command is terminal, retire the old PEL entry so it
		// is not claimed forever after the old Gateway returns.
		var current control.Command
		h := http.Header{}
		h.Set("Authorization", "Bearer "+g.devToken)
		if getErr := g.get(ctx, "/v1/commands/"+c.ID, &current, h); getErr == nil && terminalForUI(current.Status) {
			if ackErr := g.ackDelivery(ctx, m.ID); ackErr == nil {
				g.log.Debug("retired terminal pending delivery", "command_id", c.ID, "message_id", m.ID)
			} else {
				g.log.Warn("retire terminal pending delivery", "command_id", c.ID, "message_id", m.ID, "error", ackErr)
			}
		}
	}
}

type envelope struct {
	SchemaVersion int             `json:"schema_version"`
	MessageID     string          `json:"message_id"`
	Type          string          `json:"type"`
	DeviceID      string          `json:"device_id"`
	Payload       json.RawMessage `json:"payload"`
}

func push(q *relay.Outbound, typ, device string, payload any) error {
	b, _ := json.Marshal(map[string]any{"schema_version": 1, "type": typ, "device_id": device, "payload": payload})
	return q.PushReliable(b)
}
func withDevice(payload json.RawMessage, device string) (json.RawMessage, error) {
	var v map[string]any
	if len(payload) == 0 {
		v = map[string]any{}
	} else if e := json.Unmarshal(payload, &v); e != nil {
		return nil, e
	}
	v["device_id"] = device
	return json.Marshal(v)
}
func terminalForUI(s control.CommandStatus) bool {
	return s == control.StatusSucceeded || s == control.StatusFailed || s == control.StatusExpired || s == control.StatusCancelled || s == control.StatusAckTimeout || s == control.StatusRejected
}
func (g *gateway) watchCommand(ctx context.Context, q *relay.Outbound, token string, command control.Command) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(command.ExpiresAt.Add(10 * time.Second)))
	defer deadline.Stop()
	last := command.Status
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			var current control.Command
			if g.get(ctx, "/v1/commands/"+command.ID, &current, h) != nil {
				continue
			}
			if current.Status != last {
				if push(q, "command.status", current.DeviceID, current) != nil {
					return
				}
				last = current.Status
			}
			if terminalForUI(current.Status) {
				return
			}
		}
	}
}
func (g *gateway) websocket(w http.ResponseWriter, r *http.Request) {
	c, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: strings.Fields(env("WS_ORIGINS", "localhost:* 127.0.0.1:*"))})
	if e != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	q := relay.NewOutbound(64)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		for msg := range q.Reliable() {
			if c.Write(ctx, websocket.MessageText, msg) != nil {
				cancel()
				return
			}
		}
	}()
	operator, authToken, role := "", "", control.Role("")
	subscribed := false
	for {
		_, b, e := c.Read(ctx)
		if e != nil {
			return
		}
		var msg envelope
		if json.Unmarshal(b, &msg) != nil || msg.SchemaVersion != 1 {
			push(q, "error", "", map[string]string{"message": "invalid envelope"})
			continue
		}
		switch msg.Type {
		case "authenticate":
			var a struct {
				Token string `json:"token"`
			}
			json.Unmarshal(msg.Payload, &a)
			switch a.Token {
			case g.devToken:
				operator, authToken, role = "dev-operator", a.Token, control.RoleOperator
			case g.viewerToken:
				operator, authToken, role = "dev-viewer", a.Token, control.RoleViewer
			default:
				push(q, "error", "", map[string]string{"message": "invalid development token"})
				continue
			}
			push(q, "authenticated", "", map[string]any{"operator_id": operator, "role": role})
		case "subscribe":
			if operator == "" {
				push(q, "error", "", map[string]string{"message": "authenticate first"})
				continue
			}
			subscribed = true
			var devices struct {
				Devices []control.Device `json:"devices"`
			}
			h := http.Header{}
			h.Set("Authorization", "Bearer "+authToken)
			if e = g.get(ctx, "/v1/devices", &devices, h); e != nil {
				push(q, "error", "", map[string]string{"message": e.Error()})
				continue
			}
			push(q, "snapshot", "", devices)
		case "resume":
			if !subscribed {
				push(q, "error", "", map[string]string{"message": "subscribe first"})
				continue
			}
			push(q, "resume.complete", "", map[string]any{"replayed": 0, "durable_replay": false})
		case "lease.acquire", "command.request":
			if operator == "" {
				push(q, "error", msg.DeviceID, map[string]string{"message": "authenticate first"})
				continue
			}
			payload, mergeErr := withDevice(msg.Payload, msg.DeviceID)
			if mergeErr != nil || msg.DeviceID == "" {
				push(q, "error", msg.DeviceID, map[string]string{"message": "device_id is required"})
				continue
			}
			h := http.Header{}
			h.Set("X-FleetRelay-Actor-ID", operator)
			h.Set("X-FleetRelay-Actor-Role", string(role))
			if msg.Type == "lease.acquire" {
				var lease control.Lease
				e = g.post(ctx, "/v1/leases/acquire", payload, &lease, h)
				if e == nil {
					e = push(q, "lease.changed", msg.DeviceID, map[string]any{"lease": lease})
				}
			} else {
				var command control.Command
				e = g.post(ctx, "/v1/commands", payload, &command, h)
				if e == nil {
					e = push(q, "command.status", msg.DeviceID, command)
					if e == nil && !terminalForUI(command.Status) {
						go g.watchCommand(ctx, q, authToken, command)
					}
				}
			}
			if e != nil {
				push(q, "error", msg.DeviceID, map[string]string{"message": e.Error()})
			}
		default:
			push(q, "error", msg.DeviceID, map[string]string{"message": "unsupported message type"})
		}
		if errors.Is(e, relay.ErrSlowConsumer) {
			c.Close(websocket.StatusTryAgainLater, "resume required")
			return
		}
	}
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	rdb := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "redis:6379")})
	gatewayID := env("GATEWAY_ID", "gateway-a")
	g := &gateway{id: gatewayID, controlURL: env("CONTROL_API_URL", "http://control-api:8080"), internalToken: env("INTERNAL_SHARED_TOKEN", "dev-internal-only"), devToken: env("DEV_OPERATOR_TOKEN", "dev-operator-token"), viewerToken: env("DEV_VIEWER_TOKEN", "dev-viewer-token"), instanceValue: fmt.Sprintf("%s:%d:%d", gatewayID, os.Getpid(), time.Now().UnixNano()), rdb: rdb, registry: relay.NewRegistry(), log: log}
	if e := g.startLiveness(ctx, cancel); e != nil {
		log.Error("register gateway liveness", "error", e)
		os.Exit(1)
	}
	go g.consume(ctx)
	lis, e := net.Listen("tcp", env("GRPC_ADDR", ":9090"))
	if e != nil {
		panic(e)
	}
	gs := grpc.NewServer()
	fleetrelayv1.RegisterDeviceRelayServiceServer(gs, g)
	go gs.Serve(lis)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if e := rdb.Ping(r.Context()).Err(); e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		var ready map[string]any
		if e := g.get(r.Context(), "/readyz", &ready, nil); e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("fleetrelay_up 1\n")) })
	mux.HandleFunc("GET /ws", g.websocket)
	srv := &http.Server{Addr: env("HTTP_ADDR", ":8081"), Handler: mux}
	go srv.ListenAndServe()
	<-ctx.Done()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	srv.Shutdown(shutdown)
	gs.GracefulStop()
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
