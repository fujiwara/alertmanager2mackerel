package alertmanager2mackerel

import (
	"context"
	"errors"
	"net/http"

	"github.com/mackerelio/mackerel-client-go"
)

// MackerelClient is the subset of Mackerel API used by this proxy.
type MackerelClient interface {
	FindHostsContext(ctx context.Context, param *mackerel.FindHostsParam) ([]*mackerel.Host, error)
	PostCheckReportsContext(ctx context.Context, reports *mackerel.CheckReports) error
}

// isRetryable reports whether the error from Mackerel API is worth retrying by Alertmanager.
// Network errors, 429 and 5xx are retryable. Other API errors (4xx) are not.
func isRetryable(err error) bool {
	if apiErr, ok := errors.AsType[*mackerel.APIError](err); ok {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	return true
}
