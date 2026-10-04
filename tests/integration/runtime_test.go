package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/testutil"
	"github.com/google/uuid"
)

func address(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	l.Close()
	return a
}

func launch(t *testing.T, binDir, name string, env []string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(binDir, name))
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		log.Close()
		b, _ := os.ReadFile(logPath)
		if bytes.Contains(b, []byte("WARNING: DATA RACE")) {
			t.Errorf("race detector reported a race in %s", name)
		}
		if t.Failed() {
			t.Logf("%s log:\n%s", name, b)
		}
	})
}

func request(base, path string, body any, result any) error {
	method := http.MethodGet
	var data []byte
	if body != nil {
		method = http.MethodPost
		data, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer dev-operator-token")
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %d %s", path, resp.StatusCode, b)
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRealServicesDeliverCommandToSimulator(t *testing.T) {
	binDir := testutil.RequiredEnv(t, "FLEETRELAY_TEST_BIN_DIR")
	redisAddr := testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR")
	_, pool, dsn := testutil.Postgres(t)
	apiAddr, gatewayHTTP, grpcAddr, metricsAddr := address(t), address(t), address(t), address(t)
	base := "http://" + apiAddr
	common := []string{"DATABASE_URL=" + dsn, "REDIS_ADDR=" + redisAddr, "INTERNAL_SHARED_TOKEN=dev-internal-only", "DEV_OPERATOR_TOKEN=dev-operator-token"}
	launch(t, binDir, "control-api", append(append([]string{}, common...), "HTTP_ADDR="+apiAddr))
	eventually(t, "control API readiness", func() bool { return request(base, "/readyz", nil, nil) == nil })
	launch(t, binDir, "stream-gateway", append(append([]string{}, common...), "HTTP_ADDR="+gatewayHTTP, "GRPC_ADDR="+grpcAddr, "GATEWAY_ID=smoke-"+uuid.NewString(), "CONTROL_API_URL="+base))
	eventually(t, "Gateway readiness", func() bool { return request("http://"+gatewayHTTP, "/readyz", nil, nil) == nil })
	launch(t, binDir, "dispatcher", append(append([]string{}, common...), "METRICS_ADDR="+metricsAddr))
	eventually(t, "Dispatcher readiness", func() bool { return request("http://"+metricsAddr, "/readyz", nil, nil) == nil })
	deviceID := "smoke-" + uuid.NewString()
	launch(t, binDir, "device-sim", []string{"GATEWAY_GRPC_ADDR=" + grpcAddr, "DEVICE_ID=" + deviceID, "ADAPTER_DELAY_MS=25", "DROP_FIRST_ACK=false"})
	eventually(t, "online simulator", func() bool {
		var d struct {
			Devices []control.Device `json:"devices"`
		}
		if request(base, "/v1/devices", nil, &d) != nil {
			return false
		}
		for _, device := range d.Devices {
			if device.ID == deviceID && device.State == "ONLINE" {
				return true
			}
		}
		return false
	})
	var lease control.Lease
	if err := request(base, "/v1/leases/acquire", map[string]any{"device_id": deviceID}, &lease); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"device_id": deviceID, "lease_id": lease.ID, "idempotency_key": uuid.NewString(), "command_type": "PAUSE_TASK", "payload": map[string]any{}, "delivery_policy": "REQUIRE_ONLINE", "expires_at": time.Now().Add(20 * time.Second)}
	var accepted control.Command
	if err := request(base, "/v1/commands", payload, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.ID == "" {
		t.Fatal("missing command ID")
	}
	var duplicate control.Command
	if err := request(base, "/v1/commands", payload, &duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != accepted.ID {
		t.Fatal("duplicate submission created a different command")
	}
	var result control.Command
	eventually(t, "SUCCEEDED via real gRPC simulator", func() bool {
		return request(base, "/v1/commands/"+accepted.ID, nil, &result) == nil && result.Status == control.StatusSucceeded
	})
	var attempts, intents, unpublished int
	eventually(t, "delivery publication confirmation", func() bool {
		return pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE aggregate_id=$1 AND kind='command.delivery' AND published_at IS NULL`, accepted.ID).Scan(&unpublished) == nil && unpublished == 0
	})
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM command_attempts WHERE command_id=$1::uuid),
		(SELECT count(*) FROM outbox WHERE aggregate_id=$1::text AND kind='command.delivery'),
		(SELECT count(*) FROM outbox WHERE aggregate_id=$1::text AND kind='command.delivery' AND published_at IS NULL)`, accepted.ID).Scan(&attempts, &intents, &unpublished); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || intents != 1 || unpublished != 0 {
		t.Fatalf("attempts=%d intents=%d unpublished=%d", attempts, intents, unpublished)
	}
	eventually(t, "real Dispatcher HTTP metrics report drained outbox", func() bool {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + metricsAddr + "/metrics")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return err == nil && resp.StatusCode == 200 && strings.Contains(string(body), "fleetrelay_outbox_pending 0\n")
	})
	t.Logf("REST -> PostgreSQL intent -> Redis Stream -> Gateway -> gRPC simulator -> ACK: %s; duplicate same command; attempts=%d intents=%d", result.Status, attempts, intents)
}
