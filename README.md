# alertmanager2mackerel

A proxy which receives [Prometheus Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/) webhooks and posts them to [Mackerel](https://mackerel.io/) as [check monitoring](https://mackerel.io/docs/entry/custom-checks) reports of hosts.

```
Prometheus -> Alertmanager --(webhook)--> alertmanager2mackerel --(POST /api/v0/monitoring/checks/report)--> Mackerel
```

It runs as an HTTP server, or as an AWS Lambda function (Function URL / API Gateway) with the same binary.

## How it works

Each alert in a webhook is converted to a check report.

| Alertmanager | Mackerel check report |
|---|---|
| host labels (`mackerel_host_id`, `instance`, `host`) | `source` (host ID) |
| `alertname` (customizable) | `name` |
| `firing` + `severity` label | `CRITICAL` / `WARNING` / `UNKNOWN` |
| `resolved` | `OK` |
| `summary`, `description` annotations and `generatorURL` | `message` (truncated to 1024 characters) |
| time of receipt | `occurredAt` |

### Host resolution

The Mackerel host of an alert is resolved in the following order.

1. If the alert has the `mackerel_host_id` label (`--host-id-label`), its value is used as the host ID.
2. The labels in `--host-label` (default: `instance,host`) are tried in order. `:port` is stripped from the value (e.g. `web-01:9100` -> `web-01`), and the host is looked up by its name (or by its custom identifier with `--host-lookup=customIdentifier`). Lookup results are cached (`--host-cache-ttl`, `--host-negative-cache-ttl`).
3. If the host is not found, `--fallback-host-id` is used.
4. If `--fallback-host-id` is not set, the alert is dropped and a warning is logged with the whole alert, so you can find out why it was not resolved.

### Check name

Mackerel identifies a check monitoring by the pair of the host and the check name. **If multiple alerts on the same host have the same check name, resolving one of them closes the Mackerel alert even while the others are still firing.**

The default check name is `alertname` only. If an alert rule fires multiple alerts per host (e.g. per disk device), add the distinguishing labels with `--name-labels`. The labels are appended as `key=value` only when they exist in the alert.

```console
$ alertmanager2mackerel --name-labels device,mountpoint,container
# DiskFull device=sda1 mountpoint=/
```

For full control, use `--name-template` (Go template; see [Templates](#templates)).

When alerts in a single webhook share the same host and check name, the most severe status is reported and a warning is logged.

### Status

- `resolved` alerts are reported as `OK`.
- `firing` alerts are reported by the value of the `severity` label (`--severity-label`) according to `--severity-map` (default: `critical=CRITICAL,warning=WARNING`).
- Firing alerts with other (or no) severity are reported as `--default-status` (default: `CRITICAL`).

### Responses to Alertmanager

| Case | Status code | Alertmanager retries |
|---|---|---|
| Success (including dropped alerts) | 200 | - |
| Invalid payload | 400 | no |
| Invalid bearer token | 401 | no |
| Mackerel API error (network, 429, 5xx) | 503 | yes |
| Mackerel API error (other 4xx) | 422 | no |

Check reports are sent in batches of up to 100.

## Usage

```
Usage: alertmanager2mackerel --mackerel-api-key=STRING [flags]

Flags:
  -h, --help                       Show context-sensitive help.
      --listen=":8080"             Listen address (ignored on AWS Lambda) ($A2M_LISTEN)
      --webhook-path="/webhook"    Path of the webhook endpoint ($A2M_WEBHOOK_PATH)
      --auth-token=STRING          Bearer token required for the webhook endpoint (optional) ($A2M_AUTH_TOKEN)
      --mackerel-api-key=STRING    Mackerel API key ($MACKEREL_APIKEY)
      --mackerel-api-base="https://api.mackerelio.com/"
                                   Mackerel API base URL ($MACKEREL_APIBASE)
      --mackerel-timeout=10s       Timeout of each Mackerel API request ($A2M_MACKEREL_TIMEOUT)
      --host-id-label="mackerel_host_id"
                                   Label whose value is used as Mackerel host ID directly ($A2M_HOST_ID_LABEL)
      --host-label=instance,host,...
                                   Labels to look up Mackerel host (tried in order; ':port' is stripped) ($A2M_HOST_LABEL)
      --host-lookup="name"         How to look up Mackerel host by the label value ($A2M_HOST_LOOKUP)
      --fallback-host-id=STRING    Mackerel host ID used when the host cannot be resolved ($A2M_FALLBACK_HOST_ID)
      --host-cache-ttl=10m         TTL of the host lookup cache ($A2M_HOST_CACHE_TTL)
      --host-negative-cache-ttl=1m
                                   TTL of the host lookup cache for hosts not found ($A2M_HOST_NEGATIVE_CACHE_TTL)
      --name-template="{{ .Labels.alertname }}"
                                   Go template of the check monitoring name ($A2M_NAME_TEMPLATE)
      --name-labels=NAME-LABELS,...
                                   Labels appended to the check name as 'key=value' when present ($A2M_NAME_LABELS)
      --message-template="{{ with .Annotations.summary }}{{ . }}\n{{ end }}{{ with .Annotations.description }}{{ . }}\n{{ end }}{{ .GeneratorURL }}"
                                   Go template of the check message ($A2M_MESSAGE_TEMPLATE)
      --severity-label="severity"  Label which represents the severity of the alert ($A2M_SEVERITY_LABEL)
      --severity-map="critical=CRITICAL,warning=WARNING"
                                   Mapping of severity label values to check statuses ($A2M_SEVERITY_MAP)
      --default-status="CRITICAL"  Check status of firing alerts whose severity is not in the severity map ($A2M_DEFAULT_STATUS)
      --notification-interval=UINT
                                   Re-notification interval in minutes of the check monitoring (0: not set) ($A2M_NOTIFICATION_INTERVAL)
      --max-check-attempts=UINT    Max check attempts of the check monitoring (0: not set) ($A2M_MAX_CHECK_ATTEMPTS)
      --dry-run                    Log check reports instead of posting them to Mackerel ($A2M_DRY_RUN)
      --log-level="info"           Log level ($A2M_LOG_LEVEL)
      --log-format="json"          Log format ($A2M_LOG_FORMAT)
      --version                    Show version
```

The endpoints are:

- `POST /webhook` (`--webhook-path`): Alertmanager webhook receiver.
- `GET /health`: health check.

### Templates

`--name-template` and `--message-template` are Go [text/template](https://pkg.go.dev/text/template). Missing labels are rendered as empty strings. The following fields are available.

| Field | Description |
|---|---|
| `.Status` | `firing` or `resolved` |
| `.Labels` | labels of the alert (e.g. `.Labels.alertname`) |
| `.Annotations` | annotations of the alert (e.g. `.Annotations.summary`) |
| `.StartsAt`, `.EndsAt` | `time.Time` |
| `.GeneratorURL` | URL of the alert rule in Prometheus |
| `.Fingerprint` | fingerprint of the alert |
| `.Receiver` | receiver name in Alertmanager |
| `.ExternalURL` | URL of Alertmanager |

## Alertmanager configuration

```yaml
route:
  receiver: mackerel

receivers:
  - name: mackerel
    webhook_configs:
      - url: http://alertmanager2mackerel:8080/webhook
        send_resolved: true # required to close Mackerel alerts
        max_alerts: 0       # do not truncate alerts
        http_config:
          authorization:
            credentials: <same as --auth-token>
```

`send_resolved: true` is required. Without it, alerts in Mackerel are never closed.

## Deployment

### Binary

Download from [GitHub Releases](https://github.com/fujiwara/alertmanager2mackerel/releases).

```console
$ export MACKEREL_APIKEY=...
$ alertmanager2mackerel --auth-token=secret
```

### Container image

Multi-arch (linux/amd64, linux/arm64) images are available on GitHub Container Registry.

```console
$ docker run -p 8080:8080 -e MACKEREL_APIKEY=... ghcr.io/fujiwara/alertmanager2mackerel:latest
```

### AWS Lambda

alertmanager2mackerel runs as an AWS Lambda function using [ridge](https://github.com/fujiwara/ridge). It detects the Lambda runtime automatically, so no extra option is needed.

1. Download the linux binary (arm64 or amd64) and rename it to `bootstrap`.
2. Create a function with the `provided.al2023` runtime, and set the options as environment variables (`MACKEREL_APIKEY`, `A2M_AUTH_TOKEN`, ...).
3. Create a Function URL with `AuthType: NONE` and protect it with `A2M_AUTH_TOKEN`. Alertmanager can't sign requests with IAM.
4. Set the function timeout longer than `--mackerel-timeout` (it applies to each API request, and a webhook may need a host lookup and a few report requests).

Example `function.json` for [lambroll](https://github.com/fujiwara/lambroll):

```json
{
  "FunctionName": "alertmanager2mackerel",
  "Handler": "bootstrap",
  "Runtime": "provided.al2023",
  "Architectures": ["arm64"],
  "MemorySize": 128,
  "Timeout": 30,
  "Role": "arn:aws:iam::123456789012:role/alertmanager2mackerel",
  "Environment": {
    "Variables": {
      "MACKEREL_APIKEY": "{{ ssm `/alertmanager2mackerel/mackerel-apikey` }}",
      "A2M_AUTH_TOKEN": "{{ ssm `/alertmanager2mackerel/auth-token` }}",
      "A2M_FALLBACK_HOST_ID": "xxxxxxxx"
    }
  }
}
```

The host lookup cache lives only while the execution environment is warm.

## Limitations

- Alertmanager gives up retrying a webhook after a while. If a `resolved` notification is lost (e.g. the proxy is down for a long time), the Mackerel alert stays open, so close it manually.
- Check name collisions are detected only within a single webhook.

## Try it with Docker Compose

`compose.yml` runs Alertmanager, alertmanager2mackerel (built from the source) and a fake Mackerel API.

```console
$ docker compose up -d --build
```

| Service | URL | |
|---|---|---|
| Alertmanager | http://localhost:9093 | config: `compose/alertmanager.yml` |
| alertmanager2mackerel | http://localhost:8080 | |
| fake Mackerel API | http://localhost:8081 | hosts: `web-01` (`HOST-WEB01`), `db-01` (`HOST-DB01`) |

Fire an alert with `amtool` in the Alertmanager container.

```console
$ docker compose exec alertmanager amtool --alertmanager.url=http://localhost:9093 \
    alert add alertname=DiskFull instance=web-01:9100 severity=warning device=sda1 \
    --annotation='summary="disk is almost full"'
```

Resolve it by adding the same alert with `--end`.

```console
$ docker compose exec alertmanager amtool --alertmanager.url=http://localhost:9093 \
    alert add alertname=DiskFull instance=web-01:9100 severity=warning device=sda1 \
    --annotation='summary="disk is almost full"' --end=$(date -u +%Y-%m-%dT%H:%M:%SZ)
```

See the check reports received by the fake Mackerel API.

```console
$ docker compose logs -f alertmanager2mackerel fakemackerel
$ curl -s http://localhost:8081/_reports
```

An alert of an unknown host (e.g. `instance=unknown:9100`) is dropped and logged as a warning.

To post to the real Mackerel, set the API key and the API base URL, and use the host names in your organization.

```console
$ MACKEREL_APIKEY=... MACKEREL_APIBASE=https://api.mackerelio.com/ docker compose up -d
```

Stop it with `docker compose down`.

## Development

```console
$ make test      # unit tests and end-to-end tests of the binary with a fake Mackerel API
$ make test-e2e  # end-to-end tests with a real Alertmanager (requires Docker)
```

`make test-e2e` runs Alertmanager (the image in `e2e/alertmanager/Dockerfile`, kept up to date by Dependabot; override with `ALERTMANAGER_IMAGE`) in Docker with the host network, and tests the whole flow from Alertmanager to the fake Mackerel API: firing and resolved alerts, retries on Mackerel API failures, and alerts whose host cannot be resolved.

In GitHub Actions, the Alertmanager e2e tests run on the main branch and on the release pull requests created by tagpr.

## LICENSE

MIT

## Author

FUJIWARA Shunichiro
