package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCoordinatorDrainsAdmittedRequestAndRejectsNewOnes(t *testing.T) {
	coordinator := New()
	entered := make(chan struct{})
	releaseHandler := make(chan struct{})
	handler := coordinator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-releaseHandler
	}))
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/me", nil))
		close(done)
	}()
	<-entered
	coordinator.CloseAdmission()

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rejected.Code)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := coordinator.WaitDrained(ctx); err == nil {
		t.Fatal("drain completed while admitted request was still running")
	}
	close(releaseHandler)
	<-done
	if err := coordinator.WaitDrained(t.Context()); err != nil {
		t.Fatalf("wait drained: %v", err)
	}
}
