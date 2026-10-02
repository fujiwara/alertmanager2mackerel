package alertmanager2mackerel

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"

	"github.com/mackerelio/mackerel-client-go"
)

// MaxMessageLength is the maximum length (in characters) of a check report message.
const MaxMessageLength = 1024

const (
	DefaultNameTemplate    = `{{ .Labels.alertname }}`
	DefaultMessageTemplate = `{{ with .Annotations.summary }}{{ . }}
{{ end }}{{ with .Annotations.description }}{{ . }}
{{ end }}{{ .GeneratorURL }}`
)

// TemplateData is the data passed to name and message templates.
type TemplateData struct {
	Alert
	Receiver    string
	ExternalURL string
}

// Converter converts Alertmanager alerts to Mackerel check reports (except for the source).
type Converter struct {
	nameTemplate    *template.Template
	messageTemplate *template.Template
	nameLabels      []string
	severityLabel   string
	severityMap     map[string]mackerel.CheckStatus
	defaultStatus   mackerel.CheckStatus
}

// ConverterOptions are options for NewConverter.
type ConverterOptions struct {
	NameTemplate    string
	MessageTemplate string
	NameLabels      []string
	SeverityLabel   string
	SeverityMap     map[string]string
	DefaultStatus   string
}

// NewConverter creates a Converter.
func NewConverter(opt ConverterOptions) (*Converter, error) {
	nameTmpl, err := template.New("name").Option("missingkey=zero").Parse(opt.NameTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse name template: %w", err)
	}
	msgTmpl, err := template.New("message").Option("missingkey=zero").Parse(opt.MessageTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse message template: %w", err)
	}
	defaultStatus, err := parseFiringStatus(opt.DefaultStatus)
	if err != nil {
		return nil, fmt.Errorf("invalid default status: %w", err)
	}
	severityMap := make(map[string]mackerel.CheckStatus, len(opt.SeverityMap))
	for k, v := range opt.SeverityMap {
		s, err := parseFiringStatus(v)
		if err != nil {
			return nil, fmt.Errorf("invalid severity map for %q: %w", k, err)
		}
		severityMap[k] = s
	}
	return &Converter{
		nameTemplate:    nameTmpl,
		messageTemplate: msgTmpl,
		nameLabels:      opt.NameLabels,
		severityLabel:   opt.SeverityLabel,
		severityMap:     severityMap,
		defaultStatus:   defaultStatus,
	}, nil
}

func parseFiringStatus(s string) (mackerel.CheckStatus, error) {
	switch st := mackerel.CheckStatus(strings.ToUpper(s)); st {
	case mackerel.CheckStatusCritical, mackerel.CheckStatusWarning, mackerel.CheckStatusUnknown:
		return st, nil
	default:
		return "", fmt.Errorf("%q must be one of CRITICAL, WARNING or UNKNOWN", s)
	}
}

// ParseSeverityMap parses "critical=CRITICAL,warning=WARNING" style string.
func ParseSeverityMap(s string) (map[string]string, error) {
	m := map[string]string{}
	for pair := range strings.SplitSeq(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("invalid severity map entry %q (expected key=STATUS)", pair)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

// Name returns the check monitoring name for the alert.
func (c *Converter) Name(data TemplateData) (string, error) {
	var b strings.Builder
	if err := c.nameTemplate.Execute(&b, data); err != nil {
		return "", fmt.Errorf("failed to execute name template: %w", err)
	}
	name := strings.TrimSpace(b.String())
	for _, l := range c.nameLabels {
		if v, ok := data.Labels[l]; ok && v != "" {
			name += " " + l + "=" + v
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("check name is empty")
	}
	return name, nil
}

// Status returns the check status for the alert.
func (c *Converter) Status(a Alert) mackerel.CheckStatus {
	if a.Status == AlertStatusResolved {
		return mackerel.CheckStatusOK
	}
	if s, ok := c.severityMap[a.Labels[c.severityLabel]]; ok {
		return s
	}
	return c.defaultStatus
}

// Message returns the check message for the alert.
func (c *Converter) Message(data TemplateData) (string, error) {
	var b strings.Builder
	if err := c.messageTemplate.Execute(&b, data); err != nil {
		return "", fmt.Errorf("failed to execute message template: %w", err)
	}
	return truncate(strings.TrimSpace(b.String()), MaxMessageLength), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// statusSeverity orders check statuses. Higher is more severe.
func statusSeverity(s mackerel.CheckStatus) int {
	switch s {
	case mackerel.CheckStatusCritical:
		return 3
	case mackerel.CheckStatusWarning:
		return 2
	case mackerel.CheckStatusUnknown:
		return 1
	default:
		return 0
	}
}

// labelsString returns a stable string representation of labels.
func labelsString(labels map[string]string) string {
	keys := slices.Sorted(maps.Keys(labels))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
