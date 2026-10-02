package alertmanager2mackerel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mackerelio/mackerel-client-go"
)

var testNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func newTestHandler(t *testing.T, client *mockClient, opt HandlerOptions, copt ConverterOptions) *Handler {
	t.Helper()
	h := NewHandler(client, newTestConverter(t, copt), newTestResolver(t, client, HostLookupName), opt)
	h.now = func() time.Time { return testNow }
	return h
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

func postWebhook(t *testing.T, h http.Handler, msg WebhookMessage, token string) (*httptest.ResponseRecorder, webhookResult) {
	t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var res webhookResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

func hostIDOf(t *testing.T, r *mackerel.CheckReport) string {
	t.Helper()
	b, _ := json.Marshal(r.Source)
	var src struct {
		Type   string `json:"type"`
		HostID string `json:"hostId"`
	}
	json.Unmarshal(b, &src)
	if src.Type != "host" {
		t.Errorf("source type = %q", src.Type)
	}
	return src.HostID
}

func TestHandlerReports(t *testing.T) {
	client := &mockClient{hosts: map[string]string{"web-01": "host-web01", "db-01": "host-db01"}}
	h := newTestHandler(t, client, HandlerOptions{NotificationInterval: 60, MaxCheckAttempts: 3}, ConverterOptions{})
	msg := WebhookMessage{
		Version:  "4",
		Receiver: "mackerel",
		Alerts: []Alert{
			{Status: "firing", Labels: map[string]string{"alertname": "HighLoad", "instance": "web-01:9100", "severity": "warning"}, Annotations: map[string]string{"summary": "load is high"}, Fingerprint: "a"},
			{Status: "resolved", Labels: map[string]string{"alertname": "HostDown", "instance": "db-01:9100", "severity": "critical"}, Fingerprint: "b"},
		},
	}
	rec, res := postWebhook(t, h, msg, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if res.Reported != 2 || res.Dropped != 0 {
		t.Errorf("unexpected result %+v", res)
	}
	if len(client.posted) != 1 || len(client.posted[0].Reports) != 2 {
		t.Fatalf("unexpected posted %+v", client.posted)
	}
	r0, r1 := client.posted[0].Reports[0], client.posted[0].Reports[1]
	if hostIDOf(t, r0) != "host-web01" || r0.Name != "HighLoad" || r0.Status != mackerel.CheckStatusWarning || r0.Message != "load is high" {
		t.Errorf("unexpected report %+v", r0)
	}
	if r0.OccurredAt != testNow.Unix() || r0.NotificationInterval != 60 || r0.MaxCheckAttempts != 3 {
		t.Errorf("unexpected report %+v", r0)
	}
	if hostIDOf(t, r1) != "host-db01" || r1.Name != "HostDown" || r1.Status != mackerel.CheckStatusOK {
		t.Errorf("unexpected report %+v", r1)
	}
}

func TestHandlerNameCollision(t *testing.T) {
	logBuf := captureLog(t)
	client := &mockClient{hosts: map[string]string{"web-01": "host-web01"}}
	h := newTestHandler(t, client, HandlerOptions{}, ConverterOptions{})
	msg := WebhookMessage{Alerts: []Alert{
		{Status: "resolved", Labels: map[string]string{"alertname": "DiskFull", "instance": "web-01", "device": "sda1"}, Fingerprint: "a"},
		{Status: "firing", Labels: map[string]string{"alertname": "DiskFull", "instance": "web-01", "device": "sdb1", "severity": "warning"}, Fingerprint: "b"},
		{Status: "firing", Labels: map[string]string{"alertname": "DiskFull", "instance": "web-01", "device": "sdc1", "severity": "critical"}, Fingerprint: "c"},
		{Status: "firing", Labels: map[string]string{"alertname": "DiskFull", "instance": "web-01", "device": "sdd1", "severity": "warning"}, Fingerprint: "d"},
	}}
	rec, res := postWebhook(t, h, msg, "")
	if rec.Code != http.StatusOK || res.Reported != 1 {
		t.Fatalf("status = %d, result = %+v", rec.Code, res)
	}
	if r := client.posted[0].Reports[0]; r.Status != mackerel.CheckStatusCritical {
		t.Errorf("status = %s, want CRITICAL", r.Status)
	}
	if n := strings.Count(logBuf.String(), "multiple alerts share the same check name"); n != 3 {
		t.Errorf("collision warnings = %d, want 3\n%s", n, logBuf)
	}

	// --name-labels avoids collision
	client = &mockClient{hosts: map[string]string{"web-01": "host-web01"}}
	h = newTestHandler(t, client, HandlerOptions{}, ConverterOptions{NameLabels: []string{"device"}})
	_, res = postWebhook(t, h, msg, "")
	if res.Reported != 4 {
		t.Errorf("reported = %d, want 4", res.Reported)
	}
}

func TestHandlerHostNotResolved(t *testing.T) {
	msg := WebhookMessage{Receiver: "mackerel", GroupKey: "gk", Alerts: []Alert{
		{Status: "firing", Labels: map[string]string{"alertname": "HostDown", "instance": "unknown:9100"}, Annotations: map[string]string{"summary": "down"}, Fingerprint: "a"},
	}}

	t.Run("dropped", func(t *testing.T) {
		logBuf := captureLog(t)
		client := &mockClient{}
		h := newTestHandler(t, client, HandlerOptions{}, ConverterOptions{})
		rec, res := postWebhook(t, h, msg, "")
		if rec.Code != http.StatusOK || res.Dropped != 1 || res.Reported != 0 {
			t.Fatalf("status = %d, result = %+v", rec.Code, res)
		}
		if len(client.posted) != 0 {
			t.Errorf("unexpected posted %+v", client.posted)
		}
		var found bool
		for line := range strings.Lines(logBuf.String()) {
			var entry struct {
				Msg       string `json:"msg"`
				Level     string `json:"level"`
				Receiver  string `json:"receiver"`
				GroupKey  string `json:"groupKey"`
				CheckName string `json:"check_name"`
				HostValue string `json:"host_value"`
				Alert     Alert  `json:"alert"`
			}
			json.Unmarshal([]byte(line), &entry)
			if entry.Msg != "host not resolved, alert dropped" {
				continue
			}
			found = true
			if entry.Level != "WARN" || entry.Receiver != "mackerel" || entry.GroupKey != "gk" || entry.CheckName != "HostDown" || entry.HostValue != "unknown" {
				t.Errorf("unexpected log entry %s", line)
			}
			if entry.Alert.Annotations["summary"] != "down" || entry.Alert.Labels["instance"] != "unknown:9100" {
				t.Errorf("alert payload is not logged: %s", line)
			}
		}
		if !found {
			t.Errorf("warning log not found\n%s", logBuf)
		}
	})

	t.Run("fallback", func(t *testing.T) {
		client := &mockClient{}
		h := newTestHandler(t, client, HandlerOptions{FallbackHostID: "fallback"}, ConverterOptions{})
		rec, res := postWebhook(t, h, msg, "")
		if rec.Code != http.StatusOK || res.Reported != 1 {
			t.Fatalf("status = %d, result = %+v", rec.Code, res)
		}
		if id := hostIDOf(t, client.posted[0].Reports[0]); id != "fallback" {
			t.Errorf("host id = %q", id)
		}
	})
}

func TestHandlerErrors(t *testing.T) {
	msg := WebhookMessage{Alerts: []Alert{
		{Status: "firing", Labels: map[string]string{"alertname": "HostDown", "instance": "web-01"}},
	}}
	tests := []struct {
		name   string
		client *mockClient
		code   int
		purged bool
	}{
		{"find error", &mockClient{findErr: errors.New("timeout")}, http.StatusServiceUnavailable, false},
		{"post network error", &mockClient{postErr: errors.New("timeout")}, http.StatusServiceUnavailable, false},
		{"post 500", &mockClient{postErr: &mackerel.APIError{StatusCode: 500}}, http.StatusServiceUnavailable, false},
		{"post 429", &mockClient{postErr: &mackerel.APIError{StatusCode: 429}}, http.StatusServiceUnavailable, false},
		{"post 400", &mockClient{postErr: &mackerel.APIError{StatusCode: 400}}, http.StatusUnprocessableEntity, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captureLog(t)
			tt.client.hosts = map[string]string{"web-01": "host-web01"}
			h := newTestHandler(t, tt.client, HandlerOptions{}, ConverterOptions{})
			rec, _ := postWebhook(t, h, msg, "")
			if rec.Code != tt.code {
				t.Errorf("status = %d, want %d", rec.Code, tt.code)
			}
			h.resolver.mu.Lock()
			cached := len(h.resolver.cache)
			h.resolver.mu.Unlock()
			if tt.purged && cached != 0 {
				t.Errorf("cache is not purged")
			}
		})
	}
}

func TestHandlerChunk(t *testing.T) {
	client := &mockClient{hosts: map[string]string{"web-01": "host-web01"}}
	h := newTestHandler(t, client, HandlerOptions{}, ConverterOptions{NameLabels: []string{"n"}})
	var msg WebhookMessage
	for i := range 250 {
		msg.Alerts = append(msg.Alerts, Alert{Status: "firing", Labels: map[string]string{"alertname": "A", "instance": "web-01", "n": fmt.Sprint(i)}})
	}
	_, res := postWebhook(t, h, msg, "")
	if res.Reported != 250 {
		t.Errorf("reported = %d", res.Reported)
	}
	if len(client.posted) != 3 || len(client.posted[0].Reports) != 100 || len(client.posted[2].Reports) != 50 {
		t.Errorf("unexpected chunks: %d", len(client.posted))
	}
}

func TestHandlerDryRun(t *testing.T) {
	logBuf := captureLog(t)
	client := &mockClient{hosts: map[string]string{"web-01": "host-web01"}}
	h := newTestHandler(t, client, HandlerOptions{DryRun: true}, ConverterOptions{})
	msg := WebhookMessage{Alerts: []Alert{{Status: "firing", Labels: map[string]string{"alertname": "A", "instance": "web-01"}}}}
	rec, res := postWebhook(t, h, msg, "")
	if rec.Code != http.StatusOK || res.Reported != 1 || len(client.posted) != 0 {
		t.Errorf("status = %d, result = %+v, posted = %d", rec.Code, res, len(client.posted))
	}
	if !strings.Contains(logBuf.String(), `"hostId":"host-web01"`) {
		t.Errorf("dry-run log not found\n%s", logBuf)
	}
}

func TestHandlerAuthAndRouting(t *testing.T) {
	captureLog(t)
	client := &mockClient{hosts: map[string]string{"web-01": "host-web01"}}
	h := newTestHandler(t, client, HandlerOptions{AuthToken: "secret"}, ConverterOptions{})
	mux := h.Mux("/webhook")
	msg := WebhookMessage{Alerts: []Alert{{Status: "firing", Labels: map[string]string{"alertname": "A", "instance": "web-01"}}}}

	if rec, _ := postWebhook(t, mux, msg, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d", rec.Code)
	}
	if rec, _ := postWebhook(t, mux, msg, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d", rec.Code)
	}
	if rec, _ := postWebhook(t, mux, msg, "secret"); rec.Code != http.StatusOK {
		t.Errorf("valid token: status = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("{invalid"))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid payload: status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("health: status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhook", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET webhook: status = %d", rec.Code)
	}
}
