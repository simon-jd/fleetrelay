package controlhttp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/memory"
	"github.com/fleetrelay/fleetrelay/internal/controlhttp"
)

func TestInternalEndpointRequiresSharedToken(t *testing.T) {
	store := memory.New()
	svc := control.NewService(store, nil, time.Second)
	h := controlhttp.Server{Service: svc, Store: store, InternalToken: "secret", Ready: func(context.Context) error { return nil }}.Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d", res.Code)
	}
}
func TestReadinessFailsClosed(t *testing.T) {
	store := memory.New()
	svc := control.NewService(store, nil, time.Second)
	h := controlhttp.Server{Service: svc, Store: store, InternalToken: "secret", Ready: func(context.Context) error { return control.ErrUnavailable }}.Handler()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", res.Code)
	}
}
