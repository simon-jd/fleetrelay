package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

type envelope struct {
	SchemaVersion int             `json:"schema_version"`
	MessageID     string          `json:"message_id,omitempty"`
	Type          string          `json:"type"`
	DeviceID      string          `json:"device_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func send(ctx context.Context, conn *websocket.Conn, id int, typ, device string, payload any) {
	b, err := json.Marshal(payload)
	must(err)
	msg, err := json.Marshal(envelope{SchemaVersion: 1, MessageID: fmt.Sprintf("e2e-%d", id), Type: typ, DeviceID: device, Payload: b})
	must(err)
	must(conn.Write(ctx, websocket.MessageText, msg))
}

func read(ctx context.Context, conn *websocket.Conn) envelope {
	_, b, err := conn.Read(ctx)
	must(err)
	var msg envelope
	must(json.Unmarshal(b, &msg))
	fmt.Printf("ws type=%s device=%s payload=%s\n", msg.Type, msg.DeviceID, msg.Payload)
	return msg
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("Origin", "http://localhost:3000")
	conn, _, err := websocket.Dial(ctx, "ws://127.0.0.1:13000/ws", &websocket.DialOptions{HTTPHeader: headers})
	must(err)
	defer conn.CloseNow()

	send(ctx, conn, 1, "authenticate", "", map[string]any{"token": "dev-operator-token"})
	if got := read(ctx, conn); got.Type != "authenticated" {
		panic("missing authenticated response")
	}
	send(ctx, conn, 2, "subscribe", "", map[string]any{"topics": []string{"fleet", "commands", "leases"}})
	if got := read(ctx, conn); got.Type != "snapshot" {
		panic("missing snapshot response")
	}
	send(ctx, conn, 3, "lease.acquire", "vacuum-floor-3", map[string]any{"ttl_seconds": 30})
	leaseChanged := read(ctx, conn)
	if leaseChanged.Type != "lease.changed" {
		panic("missing lease.changed response")
	}
	var leasePayload struct {
		Lease struct {
			ID string `json:"lease_id"`
		} `json:"lease"`
	}
	must(json.Unmarshal(leaseChanged.Payload, &leasePayload))
	if leasePayload.Lease.ID == "" {
		panic("empty lease id")
	}

	key := fmt.Sprintf("ws-e2e-%d", time.Now().UnixNano())
	send(ctx, conn, 4, "command.request", "vacuum-floor-3", map[string]any{
		"lease_id": leasePayload.Lease.ID, "idempotency_key": key, "command_type": "PAUSE_TASK",
		"payload": map[string]any{"source": "ws-e2e"}, "delivery_policy": "REQUIRE_ONLINE",
		"expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
	})
	commandStatus := read(ctx, conn)
	if commandStatus.Type != "command.status" {
		panic("missing command.status response")
	}
	var command struct {
		ID     string `json:"command_id"`
		Status string `json:"status"`
	}
	must(json.Unmarshal(commandStatus.Payload, &command))
	if command.ID == "" {
		panic("empty command id")
	}
	fmt.Printf("command_id=%s initial_status=%s\n", command.ID, command.Status)

	for {
		followup := read(ctx, conn)
		if followup.Type != "command.status" {
			continue
		}
		var current struct {
			ID     string `json:"command_id"`
			Status string `json:"status"`
		}
		must(json.Unmarshal(followup.Payload, &current))
		if current.ID == command.ID && (current.Status == "SUCCEEDED" || current.Status == "FAILED" || current.Status == "ACK_TIMEOUT" || current.Status == "EXPIRED") {
			fmt.Printf("ws_terminal_status=%s\n", current.Status)
			return
		}
	}
}
