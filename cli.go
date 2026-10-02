package alertmanager2mackerel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/fujiwara/ridge"
	"github.com/mackerelio/mackerel-client-go"
)

// CLI is the command line options.
type CLI struct {
	Listen      string `help:"Listen address (ignored on AWS Lambda)" default:":8080" env:"A2M_LISTEN"`
	WebhookPath string `help:"Path of the webhook endpoint" default:"/webhook" env:"A2M_WEBHOOK_PATH"`
	AuthToken   string `help:"Bearer token required for the webhook endpoint (optional)" env:"A2M_AUTH_TOKEN"`

	MackerelAPIKey  string        `help:"Mackerel API key" required:"" env:"MACKEREL_APIKEY"`
	MackerelAPIBase string        `help:"Mackerel API base URL" default:"https://api.mackerelio.com/" env:"MACKEREL_APIBASE"`
	MackerelTimeout time.Duration `help:"Timeout of each Mackerel API request" default:"10s" env:"A2M_MACKEREL_TIMEOUT"`

	HostIDLabel          string        `help:"Label whose value is used as Mackerel host ID directly" default:"mackerel_host_id" env:"A2M_HOST_ID_LABEL"`
	HostLabel            []string      `help:"Labels to look up Mackerel host (tried in order; ':port' is stripped)" default:"instance,host" env:"A2M_HOST_LABEL"`
	HostLookup           string        `help:"How to look up Mackerel host by the label value" enum:"name,customIdentifier" default:"name" env:"A2M_HOST_LOOKUP"`
	FallbackHostID       string        `help:"Mackerel host ID used when the host cannot be resolved" env:"A2M_FALLBACK_HOST_ID"`
	HostCacheTTL         time.Duration `help:"TTL of the host lookup cache" default:"10m" env:"A2M_HOST_CACHE_TTL"`
	HostNegativeCacheTTL time.Duration `help:"TTL of the host lookup cache for hosts not found" default:"1m" env:"A2M_HOST_NEGATIVE_CACHE_TTL"`

	NameTemplate         string   `help:"Go template of the check monitoring name" default:"${default_name_template}" env:"A2M_NAME_TEMPLATE"`
	NameLabels           []string `help:"Labels appended to the check name as 'key=value' when present" env:"A2M_NAME_LABELS"`
	MessageTemplate      string   `help:"Go template of the check message" default:"${default_message_template}" env:"A2M_MESSAGE_TEMPLATE"`
	SeverityLabel        string   `help:"Label which represents the severity of the alert" default:"severity" env:"A2M_SEVERITY_LABEL"`
	SeverityMap          string   `help:"Mapping of severity label values to check statuses" default:"critical=CRITICAL,warning=WARNING" env:"A2M_SEVERITY_MAP"`
	DefaultStatus        string   `help:"Check status of firing alerts whose severity is not in the severity map" enum:"CRITICAL,WARNING,UNKNOWN" default:"CRITICAL" env:"A2M_DEFAULT_STATUS"`
	NotificationInterval uint     `help:"Re-notification interval in minutes of the check monitoring (0: not set)" env:"A2M_NOTIFICATION_INTERVAL"`
	MaxCheckAttempts     uint     `help:"Max check attempts of the check monitoring (0: not set)" env:"A2M_MAX_CHECK_ATTEMPTS"`

	DryRun    bool             `help:"Log check reports instead of posting them to Mackerel" env:"A2M_DRY_RUN"`
	LogLevel  string           `help:"Log level" enum:"debug,info,warn,error" default:"info" env:"A2M_LOG_LEVEL"`
	LogFormat string           `help:"Log format" enum:"text,json" default:"json" env:"A2M_LOG_FORMAT"`
	Version   kong.VersionFlag `help:"Show version"`
}

// Run parses the command line options and runs the proxy.
func Run(ctx context.Context) error {
	var cli CLI
	kong.Parse(&cli,
		kong.Name("alertmanager2mackerel"),
		kong.Description("A proxy which receives Alertmanager webhooks and posts check monitoring reports to Mackerel."),
		kong.Vars{
			"version":                  Version,
			"default_name_template":    DefaultNameTemplate,
			"default_message_template": DefaultMessageTemplate,
		},
	)
	return cli.Run(ctx)
}

// Run runs the proxy with the options.
func (cli *CLI) Run(ctx context.Context) error {
	setupLogger(cli.LogLevel, cli.LogFormat)

	handler, err := cli.NewHandler()
	if err != nil {
		return err
	}
	if cli.FallbackHostID == "" {
		slog.Warn("fallback-host-id is not set; alerts whose host cannot be resolved will be dropped")
	}
	if cli.DryRun {
		slog.Warn("dry-run mode; check reports are not posted to Mackerel")
	}
	slog.Info("starting alertmanager2mackerel", "version", Version, "listen", cli.Listen, "webhook_path", cli.WebhookPath, "lambda", ridge.AsLambdaHandler())
	ridge.RunWithContext(ctx, cli.Listen, "/", handler.Mux(cli.WebhookPath))
	return nil
}

// NewHandler creates a Handler from the options.
func (cli *CLI) NewHandler() (*Handler, error) {
	client, err := mackerel.NewClientWithOptions(cli.MackerelAPIKey, cli.MackerelAPIBase, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mackerel client: %w", err)
	}
	client.HTTPClient.Timeout = cli.MackerelTimeout
	client.UserAgent = "alertmanager2mackerel/" + Version

	severityMap, err := ParseSeverityMap(cli.SeverityMap)
	if err != nil {
		return nil, err
	}
	converter, err := NewConverter(ConverterOptions{
		NameTemplate:    cli.NameTemplate,
		MessageTemplate: cli.MessageTemplate,
		NameLabels:      cli.NameLabels,
		SeverityLabel:   cli.SeverityLabel,
		SeverityMap:     severityMap,
		DefaultStatus:   cli.DefaultStatus,
	})
	if err != nil {
		return nil, err
	}
	resolver, err := NewHostResolver(client, HostResolverOptions{
		HostIDLabel: cli.HostIDLabel,
		HostLabels:  cli.HostLabel,
		Lookup:      cli.HostLookup,
		TTL:         cli.HostCacheTTL,
		NegativeTTL: cli.HostNegativeCacheTTL,
	})
	if err != nil {
		return nil, err
	}
	return NewHandler(client, converter, resolver, HandlerOptions{
		FallbackHostID:       cli.FallbackHostID,
		AuthToken:            cli.AuthToken,
		DryRun:               cli.DryRun,
		NotificationInterval: cli.NotificationInterval,
		MaxCheckAttempts:     cli.MaxCheckAttempts,
	}), nil
}

func setupLogger(level, format string) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}
