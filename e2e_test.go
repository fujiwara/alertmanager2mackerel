package alertmanager2mackerel_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fujiwara/alertmanager2mackerel/internal/e2eutil"
	"github.com/fujiwara/alertmanager2mackerel/internal/fakemackerel"
)

// End-to-end tests running the built binary against a fake Mackerel API.

const testAPIKey = "e2e-api-key"

func startFakeMackerel(t *testing.T, hosts ...fakemackerel.Host) *fakemackerel.Server {
	t.Helper()
	if testing.Short() {
		t.Skip("skip e2e test in short mode")
	}
	s, err := fakemackerel.New(testAPIKey, "", hosts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func proxyEnv(s *fakemackerel.Server, extra ...string) []string {
	return append([]string{"MACKEREL_APIKEY=" + testAPIKey, "MACKEREL_APIBASE=" + s.URL + "/"}, extra...)
}

type webhookAlert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
}

func sendWebhook(t *testing.T, p *e2eutil.Proxy, token string, alerts ...webhookAlert) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"version":  "4",
		"receiver": "mackerel",
		"groupKey": "e2e",
		"status":   "firing",
		"alerts":   alerts,
	})
	req, _ := http.NewRequest(http.MethodPost, p.URL+"/webhook", bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res map[string]any
	json.NewDecoder(resp.Body).Decode(&res)
	return resp.StatusCode, res
}

func firing(name, instance, severity string, extra ...string) webhookAlert {
	labels := map[string]string{"alertname": name, "instance": instance, "severity": severity}
	for i := 0; i+1 < len(extra); i += 2 {
		labels[extra[i]] = extra[i+1]
	}
	return webhookAlert{Status: "firing", Labels: labels, Annotations: map[string]string{"summary": name + " on " + instance}}
}

func resolved(a webhookAlert) webhookAlert {
	a.Status = "resolved"
	return a
}

func TestE2EFiringAndResolved(t *testing.T) {
	fm := startFakeMackerel(t, fakemackerel.Host{ID: "HOST-WEB01", Name: "web-01"})
	p := e2eutil.StartProxy(t, e2eutil.FreeAddr(t),
		proxyEnv(fm, "A2M_AUTH_TOKEN=secret", "A2M_NAME_LABELS=device", "A2M_NOTIFICATION_INTERVAL=30"),
	)

	sda := firing("DiskFull", "web-01:9100", "warning", "device", "sda1")
	sdb := firing("DiskFull", "web-01:9100", "critical", "device", "sdb1")

	if code, _ := sendWebhook(t, p, "", sda); code != http.StatusUnauthorized {
		t.Errorf("without token: status = %d", code)
	}
	if code, res := sendWebhook(t, p, "secret", sda, sdb); code != http.StatusOK || res["reported"] != float64(2) {
		t.Fatalf("firing: status = %d, result = %v", code, res)
	}
	if code, res := sendWebhook(t, p, "secret", resolved(sda)); code != http.StatusOK || res["reported"] != float64(1) {
		t.Fatalf("resolved: status = %d, result = %v", code, res)
	}

	reports := fm.Reports()
	want := []struct{ name, status string }{
		{"DiskFull device=sda1", "WARNING"},
		{"DiskFull device=sdb1", "CRITICAL"},
		{"DiskFull device=sda1", "OK"},
	}
	if len(reports) != len(want) {
		t.Fatalf("reports = %+v", reports)
	}
	for i, w := range want {
		r := reports[i]
		if r.Source.Type != "host" || r.Source.HostID != "HOST-WEB01" || r.Name != w.name || r.Status != w.status {
			t.Errorf("report[%d] = %+v, want %s %s", i, r, w.name, w.status)
		}
		if r.NotificationInterval != 30 || r.OccurredAt == 0 {
			t.Errorf("report[%d] = %+v", i, r)
		}
	}
	if !strings.HasPrefix(reports[0].Message, "DiskFull on web-01:9100") {
		t.Errorf("message = %q", reports[0].Message)
	}
	// The host is looked up once by the name without port, and cached.
	if lookups := fm.HostLookups(); len(lookups) != 1 || lookups[0] != "name=web-01" {
		t.Errorf("host lookups = %v", lookups)
	}
	if n := fm.Unauthorized(); n != 0 {
		t.Errorf("unauthorized requests = %d", n)
	}
}

func TestE2EMackerelErrors(t *testing.T) {
	fm := startFakeMackerel(t, fakemackerel.Host{ID: "HOST-WEB01", Name: "web-01"})
	p := e2eutil.StartProxy(t, e2eutil.FreeAddr(t), proxyEnv(fm))
	alert := firing("HostDown", "web-01:9100", "critical")

	for _, code := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		fm.FailNextPosts(code)
		if got, _ := sendWebhook(t, p, "", alert); got != http.StatusServiceUnavailable {
			t.Errorf("mackerel %d: status = %d, want 503", code, got)
		}
	}

	// non-retryable error purges the host cache
	fm.FailNextPosts(http.StatusBadRequest)
	if got, _ := sendWebhook(t, p, "", alert); got != http.StatusUnprocessableEntity {
		t.Errorf("mackerel 400: status = %d, want 422", got)
	}
	before := len(fm.HostLookups())
	if got, _ := sendWebhook(t, p, "", alert); got != http.StatusOK {
		t.Errorf("after recovery: status = %d", got)
	}
	if after := len(fm.HostLookups()); after != before+1 {
		t.Errorf("host was not looked up again: lookups %d -> %d", before, after)
	}
	if n := len(fm.Reports()); n != 1 {
		t.Errorf("reports = %d, want 1", n)
	}
	p.WaitLog(t, `"msg":"failed to post check reports"`, time.Second)
}

func TestE2EInvalidAPIKey(t *testing.T) {
	fm := startFakeMackerel(t, fakemackerel.Host{ID: "HOST-WEB01", Name: "web-01"})
	p := e2eutil.StartProxy(t, e2eutil.FreeAddr(t), []string{"MACKEREL_APIKEY=wrong", "MACKEREL_APIBASE=" + fm.URL + "/"},
		"--host-id-label", "mackerel_host_id")
	alert := firing("HostDown", "web-01", "critical", "mackerel_host_id", "HOST-WEB01")
	if got, _ := sendWebhook(t, p, "", alert); got != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", got)
	}
	if fm.Unauthorized() == 0 {
		t.Error("API key was not checked")
	}
}

func TestE2EHostResolution(t *testing.T) {
	fm := startFakeMackerel(t,
		fakemackerel.Host{ID: "HOST-EC2", Name: "ip-10-0-0-1", CustomIdentifier: "i-0123"},
	)

	t.Run("customIdentifier", func(t *testing.T) {
		p := e2eutil.StartProxy(t, e2eutil.FreeAddr(t), proxyEnv(fm), "--host-lookup", "customIdentifier", "--host-label", "instance_id")
		alert := firing("HighCPU", "10.0.0.1:9100", "warning", "instance_id", "i-0123")
		if code, res := sendWebhook(t, p, "", alert); code != http.StatusOK || res["reported"] != float64(1) {
			t.Fatalf("status = %d, result = %v", code, res)
		}
		if r := fm.Reports(); r[len(r)-1].Source.HostID != "HOST-EC2" {
			t.Errorf("report = %+v", r[len(r)-1])
		}
	})

	t.Run("dropped", func(t *testing.T) {
		p := e2eutil.StartProxy(t, e2eutil.FreeAddr(t), proxyEnv(fm))
		p.WaitLog(t, "fallback-host-id is not set", time.Second)
		alert := firing("HostDown", "unknown:9100", "critical")
		if code, res := sendWebhook(t, p, "", alert); code != http.StatusOK || res["dropped"] != float64(1) {
			t.Fatalf("status = %d, result = %v", code, res)
		}
		p.WaitLog(t, `"msg":"host not resolved, alert dropped"`, time.Second)
		p.WaitLog(t, `"alert":{"status":"firing","labels":{"alertname":"HostDown"`, time.Second)
	})

	t.Run("fallback", func(t *testing.T) {
		before := len(fm.Reports())
		p := e2eutil.StartProxy(t, e2eutil.FreeAddr(t), proxyEnv(fm, "A2M_FALLBACK_HOST_ID=HOST-FALLBACK"))
		alert := firing("HostDown", "unknown:9100", "critical")
		if code, res := sendWebhook(t, p, "", alert); code != http.StatusOK || res["reported"] != float64(1) {
			t.Fatalf("status = %d, result = %v", code, res)
		}
		r := fm.Reports()
		if len(r) != before+1 || r[len(r)-1].Source.HostID != "HOST-FALLBACK" {
			t.Errorf("reports = %+v", r)
		}
		if strings.Contains(p.Log(), "fallback-host-id is not set") {
			t.Error("unexpected warning about fallback-host-id")
		}
	})
}

func TestE2EDryRun(t *testing.T) {
	fm := startFakeMackerel(t, fakemackerel.Host{ID: "HOST-WEB01", Name: "web-01"})
	p := e2eutil.StartProxy(t, e2eutil.FreeAddr(t), proxyEnv(fm), "--dry-run")
	if code, res := sendWebhook(t, p, "", firing("HostDown", "web-01:9100", "critical")); code != http.StatusOK || res["reported"] != float64(1) {
		t.Fatalf("status = %d, result = %v", code, res)
	}
	if n := fm.PostRequests(); n != 0 {
		t.Errorf("post requests = %d, want 0", n)
	}
	p.WaitLog(t, `"hostId":"HOST-WEB01"`, time.Second)
}

func TestE2EInvalidOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e test in short mode")
	}
	tests := []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{"no api key", nil, nil, "--mackerel-api-key"},
		{"invalid severity map", []string{"MACKEREL_APIKEY=x"}, []string{"--severity-map", "critical"}, "invalid severity map"},
		{"invalid template", []string{"MACKEREL_APIKEY=x"}, []string{"--name-template", "{{"}, "failed to parse name template"},
		{"invalid status", []string{"MACKEREL_APIKEY=x", "A2M_DEFAULT_STATUS=OK"}, nil, "--default-status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := append([]string{"MACKEREL_APIKEY="}, tt.env...)
			out, err := e2eutil.Run(t, env, tt.args...)
			if err == nil {
				t.Fatalf("expected error, output:\n%s", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("output does not contain %q:\n%s", tt.want, out)
			}
		})
	}

	out, err := e2eutil.Run(t, nil, "--version")
	if err != nil || !strings.HasPrefix(out, "v") {
		t.Errorf("--version: %v %q", err, out)
	}
}
