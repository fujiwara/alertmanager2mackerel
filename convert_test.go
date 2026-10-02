package alertmanager2mackerel

import (
	"strings"
	"testing"

	"github.com/mackerelio/mackerel-client-go"
)

func newTestConverter(t *testing.T, opt ConverterOptions) *Converter {
	t.Helper()
	if opt.NameTemplate == "" {
		opt.NameTemplate = DefaultNameTemplate
	}
	if opt.MessageTemplate == "" {
		opt.MessageTemplate = DefaultMessageTemplate
	}
	if opt.SeverityLabel == "" {
		opt.SeverityLabel = "severity"
	}
	if opt.SeverityMap == nil {
		opt.SeverityMap = map[string]string{"critical": "CRITICAL", "warning": "WARNING"}
	}
	if opt.DefaultStatus == "" {
		opt.DefaultStatus = "CRITICAL"
	}
	c, err := NewConverter(opt)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConverterName(t *testing.T) {
	tests := []struct {
		name   string
		opt    ConverterOptions
		labels map[string]string
		want   string
		err    bool
	}{
		{
			name:   "default",
			labels: map[string]string{"alertname": "DiskFull", "device": "sda1"},
			want:   "DiskFull",
		},
		{
			name:   "name labels",
			opt:    ConverterOptions{NameLabels: []string{"device", "mountpoint"}},
			labels: map[string]string{"alertname": "DiskFull", "device": "sda1"},
			want:   "DiskFull device=sda1",
		},
		{
			name:   "custom template",
			opt:    ConverterOptions{NameTemplate: `{{ .Labels.job }}/{{ .Labels.alertname }}`},
			labels: map[string]string{"alertname": "HostDown", "job": "node"},
			want:   "node/HostDown",
		},
		{
			name:   "missing label in template",
			opt:    ConverterOptions{NameTemplate: `{{ .Labels.alertname }}{{ .Labels.nothing }}`},
			labels: map[string]string{"alertname": "HostDown"},
			want:   "HostDown",
		},
		{
			name:   "empty",
			labels: map[string]string{},
			err:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestConverter(t, tt.opt)
			got, err := c.Name(TemplateData{Alert: Alert{Labels: tt.labels}})
			if tt.err {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConverterStatus(t *testing.T) {
	c := newTestConverter(t, ConverterOptions{DefaultStatus: "unknown"})
	tests := []struct {
		alert Alert
		want  mackerel.CheckStatus
	}{
		{Alert{Status: "firing", Labels: map[string]string{"severity": "critical"}}, mackerel.CheckStatusCritical},
		{Alert{Status: "firing", Labels: map[string]string{"severity": "warning"}}, mackerel.CheckStatusWarning},
		{Alert{Status: "firing", Labels: map[string]string{"severity": "info"}}, mackerel.CheckStatusUnknown},
		{Alert{Status: "firing", Labels: map[string]string{}}, mackerel.CheckStatusUnknown},
		{Alert{Status: "resolved", Labels: map[string]string{"severity": "critical"}}, mackerel.CheckStatusOK},
	}
	for _, tt := range tests {
		if got := c.Status(tt.alert); got != tt.want {
			t.Errorf("Status(%v) = %s, want %s", tt.alert, got, tt.want)
		}
	}
}

func TestConverterMessage(t *testing.T) {
	c := newTestConverter(t, ConverterOptions{})
	got, err := c.Message(TemplateData{Alert: Alert{
		Annotations:  map[string]string{"summary": "disk full", "description": "sda1 is 95% used"},
		GeneratorURL: "http://prometheus/graph",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "disk full\nsda1 is 95% used\nhttp://prometheus/graph"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	got, err = c.Message(TemplateData{Alert: Alert{GeneratorURL: "http://prometheus/graph"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://prometheus/graph"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	got, err = c.Message(TemplateData{Alert: Alert{Annotations: map[string]string{"summary": strings.Repeat("あ", 2000)}}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got)); n != MaxMessageLength {
		t.Errorf("message length = %d, want %d", n, MaxMessageLength)
	}
}

func TestNewConverterInvalid(t *testing.T) {
	for _, opt := range []ConverterOptions{
		{NameTemplate: "{{", MessageTemplate: "", DefaultStatus: "CRITICAL"},
		{NameTemplate: "", MessageTemplate: "{{", DefaultStatus: "CRITICAL"},
		{DefaultStatus: "OK"},
		{DefaultStatus: "CRITICAL", SeverityMap: map[string]string{"critical": "FATAL"}},
	} {
		if _, err := NewConverter(opt); err == nil {
			t.Errorf("expected error for %+v", opt)
		}
	}
}

func TestParseSeverityMap(t *testing.T) {
	m, err := ParseSeverityMap(" critical=CRITICAL, warning = WARNING ,")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["critical"] != "CRITICAL" || m["warning"] != "WARNING" {
		t.Errorf("unexpected map %v", m)
	}
	if _, err := ParseSeverityMap("critical"); err == nil {
		t.Error("expected error")
	}
}
