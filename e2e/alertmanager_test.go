//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fujiwara/alertmanager2mackerel/internal/e2eutil"
	"github.com/fujiwara/alertmanager2mackerel/internal/fakemackerel"
)

const (
	apiKey      = "e2e-api-key"
	authToken   = "e2e-auth-token"
	waitTimeout = 30 * time.Second
)

const alertmanagerConfig = `
route:
  receiver: mackerel
  group_by: ["alertname", "instance"]
  group_wait: 1s
  group_interval: 1s
  repeat_interval: 1h
receivers:
  - name: mackerel
    webhook_configs:
      - url: %s/webhook
        send_resolved: true
        max_alerts: 0
        http_config:
          authorization:
            credentials: %s
`

// alertmanagerImage returns the image from ALERTMANAGER_IMAGE or the FROM line of alertmanager/Dockerfile,
// which is kept up to date by Dependabot.
func alertmanagerImage(t *testing.T) string {
	t.Helper()
	if image := os.Getenv("ALERTMANAGER_IMAGE"); image != "" {
		return image
	}
	b, err := os.ReadFile(filepath.Join("alertmanager", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(b)) {
		if fields := strings.Fields(line); len(fields) >= 2 && strings.EqualFold(fields[0], "FROM") {
			return fields[1]
		}
	}
	t.Fatal("FROM not found in alertmanager/Dockerfile")
	return ""
}

// startAlertmanager runs Alertmanager in Docker with host network.
func startAlertmanager(t *testing.T, webhookBaseURL string) string {
	t.Helper()
	image := alertmanagerImage(t)
	dir := t.TempDir()
	// Alertmanager runs as nobody in the container.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(alertmanagerConfig, webhookBaseURL, authToken)
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	addr := e2eutil.FreeAddr(t)
	name := fmt.Sprintf("a2m-e2e-alertmanager-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "host",
		"-v", dir+":/etc/alertmanager:ro",
		image,
		"--config.file=/etc/alertmanager/config.yml",
		"--storage.path=/tmp/alertmanager",
		"--web.listen-address="+addr,
		"--cluster.listen-address=",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to start alertmanager: %s\n%s", err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("alertmanager log:\n%s", logs)
		}
		exec.Command("docker", "rm", "-f", name).Run()
	})
	url := "http://" + addr
	e2eutil.Eventually(t, waitTimeout, func() bool {
		resp, err := http.Get(url + "/-/ready")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "alertmanager did not become ready")
	return url
}

type amAlert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt,omitzero"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

func postAlerts(t *testing.T, amURL string, alerts ...amAlert) {
	t.Helper()
	b, _ := json.Marshal(alerts)
	resp, err := http.Post(amURL+"/api/v2/alerts", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to post alerts: %s", resp.Status)
	}
}

func newAlert(startsAt time.Time, labels ...string) amAlert {
	a := amAlert{
		Labels:       map[string]string{},
		Annotations:  map[string]string{"summary": "e2e test alert"},
		StartsAt:     startsAt,
		GeneratorURL: "http://prometheus.example.com/graph",
	}
	for i := 0; i+1 < len(labels); i += 2 {
		a.Labels[labels[i]] = labels[i+1]
	}
	return a
}

func waitReport(t *testing.T, fm *fakemackerel.Server, hostID, name, status string) {
	t.Helper()
	e2eutil.Eventually(t, waitTimeout, func() bool {
		for _, r := range fm.Reports() {
			if r.Source.HostID == hostID && r.Name == name && r.Status == status {
				return true
			}
		}
		return false
	}, "report not received: host=%s name=%q status=%s\nreports: %+v", hostID, name, status, fm.Reports())
}

func TestAlertmanager(t *testing.T) {
	fm, err := fakemackerel.New(apiKey, "",
		fakemackerel.Host{ID: "HOST-WEB01", Name: "web-01"},
		fakemackerel.Host{ID: "HOST-DB01", Name: "db-01"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fm.Close)

	proxy := e2eutil.StartProxy(t, e2eutil.FreeAddr(t),
		[]string{
			"MACKEREL_APIKEY=" + apiKey,
			"MACKEREL_APIBASE=" + fm.URL + "/",
			"A2M_AUTH_TOKEN=" + authToken,
		},
		"--name-labels", "device",
	)
	amURL := startAlertmanager(t, proxy.URL)
	now := time.Now()

	hostDown := newAlert(now, "alertname", "HostDown", "instance", "db-01:9100", "severity", "critical")
	sda := newAlert(now, "alertname", "DiskFull", "instance", "web-01:9100", "severity", "warning", "device", "sda1")
	sdb := newAlert(now, "alertname", "DiskFull", "instance", "web-01:9100", "severity", "critical", "device", "sdb1")

	t.Run("firing", func(t *testing.T) {
		postAlerts(t, amURL, hostDown, sda, sdb)
		waitReport(t, fm, "HOST-DB01", "HostDown", "CRITICAL")
		waitReport(t, fm, "HOST-WEB01", "DiskFull device=sda1", "WARNING")
		waitReport(t, fm, "HOST-WEB01", "DiskFull device=sdb1", "CRITICAL")
		for _, r := range fm.Reports() {
			if !strings.Contains(r.Message, "e2e test alert") || !strings.Contains(r.Message, "http://prometheus.example.com/graph") {
				t.Errorf("unexpected message %q", r.Message)
			}
		}
	})

	t.Run("resolved", func(t *testing.T) {
		resolvedSda := sda
		resolvedSda.EndsAt = time.Now()
		postAlerts(t, amURL, resolvedSda)
		waitReport(t, fm, "HOST-WEB01", "DiskFull device=sda1", "OK")
		for _, r := range fm.Reports() {
			if r.Name == "DiskFull device=sdb1" && r.Status == "OK" {
				t.Errorf("sdb1 must not be resolved: %+v", r)
			}
		}
	})

	t.Run("retry on Mackerel API failure", func(t *testing.T) {
		before := fm.PostRequests()
		fm.FailNextPosts(http.StatusInternalServerError, http.StatusServiceUnavailable)
		postAlerts(t, amURL, newAlert(time.Now(), "alertname", "HighLoad", "instance", "web-01:9100", "severity", "warning"))
		waitReport(t, fm, "HOST-WEB01", "HighLoad", "WARNING")
		if n := fm.PostRequests() - before; n < 3 {
			t.Errorf("post requests = %d, want >= 3 (2 failures and a success)", n)
		}
	})

	t.Run("host not resolved", func(t *testing.T) {
		postAlerts(t, amURL, newAlert(time.Now(), "alertname", "HostDown", "instance", "unknown:9100", "severity", "critical"))
		proxy.WaitLog(t, `"msg":"host not resolved, alert dropped"`, waitTimeout)
		proxy.WaitLog(t, `"host_value":"unknown"`, waitTimeout)
	})

	if n := fm.Unauthorized(); n != 0 {
		t.Errorf("unauthorized requests to Mackerel = %d", n)
	}
}
