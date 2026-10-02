package alertmanager2mackerel

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/mackerelio/mackerel-client-go"
)

const (
	// MaxReportsPerRequest is the maximum number of check reports in a single API request.
	MaxReportsPerRequest = 100

	maxRequestBodySize = 10 << 20
)

// Handler is the HTTP handler which receives Alertmanager webhooks and posts check reports to Mackerel.
type Handler struct {
	client               MackerelClient
	converter            *Converter
	resolver             *HostResolver
	fallbackHostID       string
	authToken            string
	dryRun               bool
	notificationInterval uint
	maxCheckAttempts     uint
	now                  func() time.Time
}

// HandlerOptions are options for NewHandler.
type HandlerOptions struct {
	FallbackHostID       string
	AuthToken            string
	DryRun               bool
	NotificationInterval uint
	MaxCheckAttempts     uint
}

// NewHandler creates a Handler.
func NewHandler(client MackerelClient, converter *Converter, resolver *HostResolver, opt HandlerOptions) *Handler {
	return &Handler{
		client:               client,
		converter:            converter,
		resolver:             resolver,
		fallbackHostID:       opt.FallbackHostID,
		authToken:            opt.AuthToken,
		dryRun:               opt.DryRun,
		notificationInterval: opt.NotificationInterval,
		maxCheckAttempts:     opt.MaxCheckAttempts,
		now:                  time.Now,
	}
}

// Mux returns http.Handler serving the webhook endpoint and the health check endpoint.
func (h *Handler) Mux(webhookPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	})
	mux.Handle("POST "+webhookPath, h)
	return mux
}

type webhookResult struct {
	Reported int    `json:"reported"`
	Dropped  int    `json:"dropped"`
	Error    string `json:"error,omitempty"`
}

type pendingReport struct {
	report   *mackerel.CheckReport
	identity string
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeResult(w, http.StatusUnauthorized, webhookResult{Error: "unauthorized"})
		return
	}
	var msg WebhookMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodySize)).Decode(&msg); err != nil {
		slog.Warn("failed to decode webhook payload", "error", err)
		writeResult(w, http.StatusBadRequest, webhookResult{Error: "invalid payload"})
		return
	}
	logger := slog.With("receiver", msg.Receiver, "groupKey", msg.GroupKey)
	if msg.TruncatedAlerts > 0 {
		logger.Warn("alerts are truncated by Alertmanager; set max_alerts: 0 in webhook_config", "truncatedAlerts", msg.TruncatedAlerts)
	}

	ctx := r.Context()
	now := h.now()
	var result webhookResult
	var order []string
	pending := map[string]*pendingReport{}
	for _, alert := range msg.Alerts {
		data := TemplateData{Alert: alert, Receiver: msg.Receiver, ExternalURL: msg.ExternalURL}
		name, err := h.converter.Name(data)
		if err != nil {
			logger.Warn("failed to build check name, alert dropped", "error", err, "alert", jsonLog{alert})
			result.Dropped++
			continue
		}
		status := h.converter.Status(alert)
		message, err := h.converter.Message(data)
		if err != nil {
			logger.Warn("failed to build check message", "error", err, "check_name", name)
		}

		res, err := h.resolver.Resolve(ctx, alert.Labels)
		if err != nil {
			logger.Error("failed to resolve host", "error", err)
			writeResult(w, http.StatusServiceUnavailable, webhookResult{Error: "failed to resolve host"})
			return
		}
		hostID := res.HostID
		if hostID == "" {
			hostID = h.fallbackHostID
		}
		if hostID == "" {
			logger.Warn("host not resolved, alert dropped",
				"check_name", name, "check_status", status,
				"host_label", res.Label, "host_value", res.Value,
				"alert", jsonLog{alert})
			result.Dropped++
			continue
		}

		report := &mackerel.CheckReport{
			Source:               mackerel.NewCheckSourceHost(hostID),
			Name:                 name,
			Status:               status,
			Message:              message,
			OccurredAt:           now.Unix(),
			NotificationInterval: h.notificationInterval,
			MaxCheckAttempts:     h.maxCheckAttempts,
		}
		identity := alert.Fingerprint
		if identity == "" {
			identity = labelsString(alert.Labels)
		}
		key := hostID + "\x00" + name
		if prev, ok := pending[key]; ok {
			if prev.identity != identity {
				logger.Warn("multiple alerts share the same check name on the host; consider --name-labels or --name-template",
					"host_id", hostID, "check_name", name, "alert", jsonLog{alert})
			}
			if statusSeverity(status) > statusSeverity(prev.report.Status) {
				pending[key] = &pendingReport{report: report, identity: identity}
			}
			continue
		}
		pending[key] = &pendingReport{report: report, identity: identity}
		order = append(order, key)
	}

	reports := make([]*mackerel.CheckReport, 0, len(order))
	for _, key := range order {
		reports = append(reports, pending[key].report)
	}
	for chunk := range slices.Chunk(reports, MaxReportsPerRequest) {
		if h.dryRun {
			for _, rep := range chunk {
				logger.Info("dry-run: check report", "report", jsonLog{rep})
			}
			result.Reported += len(chunk)
			continue
		}
		if err := h.client.PostCheckReportsContext(ctx, &mackerel.CheckReports{Reports: chunk}); err != nil {
			if isRetryable(err) {
				logger.Error("failed to post check reports (retryable)", "error", err)
				result.Error = "failed to post check reports"
				writeResult(w, http.StatusServiceUnavailable, result)
				return
			}
			// The cached host may be retired or deleted. Purge the cache to look up again next time.
			h.resolver.Purge()
			logger.Error("failed to post check reports", "error", err, "reports", jsonLog{chunk})
			result.Error = "failed to post check reports"
			writeResult(w, http.StatusUnprocessableEntity, result)
			return
		}
		result.Reported += len(chunk)
	}
	logger.Info("processed webhook", "alerts", len(msg.Alerts), "reported", result.Reported, "dropped", result.Dropped)
	writeResult(w, http.StatusOK, result)
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.authToken == "" {
		return true
	}
	expected := "Bearer " + h.authToken
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) == 1
}

func writeResult(w http.ResponseWriter, code int, res webhookResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(res)
}

// jsonLog logs the value as JSON in both text and JSON log handlers.
type jsonLog struct {
	v any
}

func (j jsonLog) LogValue() slog.Value {
	b, err := json.Marshal(j.v)
	if err != nil {
		return slog.StringValue(fmt.Sprintf("%+v", j.v))
	}
	return slog.AnyValue(json.RawMessage(b))
}
