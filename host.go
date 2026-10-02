package alertmanager2mackerel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mackerelio/mackerel-client-go"
)

const (
	HostLookupName             = "name"
	HostLookupCustomIdentifier = "customIdentifier"
)

// HostResolution is the result of resolving the Mackerel host of an alert.
type HostResolution struct {
	HostID string
	// Label and Value are the label used for resolution (or the last tried label when not resolved).
	Label string
	Value string
}

// HostResolver resolves Mackerel host ID from alert labels.
type HostResolver struct {
	client      MackerelClient
	hostIDLabel string
	hostLabels  []string
	lookup      string
	ttl         time.Duration
	negativeTTL time.Duration
	now         func() time.Time

	mu    sync.Mutex
	cache map[string]hostCacheEntry
}

type hostCacheEntry struct {
	hostID  string
	expires time.Time
}

// HostResolverOptions are options for NewHostResolver.
type HostResolverOptions struct {
	HostIDLabel string
	HostLabels  []string
	Lookup      string
	TTL         time.Duration
	NegativeTTL time.Duration
}

// NewHostResolver creates a HostResolver.
func NewHostResolver(client MackerelClient, opt HostResolverOptions) (*HostResolver, error) {
	switch opt.Lookup {
	case HostLookupName, HostLookupCustomIdentifier:
	default:
		return nil, fmt.Errorf("invalid host lookup %q (must be %s or %s)", opt.Lookup, HostLookupName, HostLookupCustomIdentifier)
	}
	return &HostResolver{
		client:      client,
		hostIDLabel: opt.HostIDLabel,
		hostLabels:  opt.HostLabels,
		lookup:      opt.Lookup,
		ttl:         opt.TTL,
		negativeTTL: opt.NegativeTTL,
		now:         time.Now,
		cache:       map[string]hostCacheEntry{},
	}, nil
}

// Resolve resolves the Mackerel host ID of the alert labels.
// It returns a resolution with empty HostID when the host is not found.
// An error is returned only when Mackerel API fails.
func (r *HostResolver) Resolve(ctx context.Context, labels map[string]string) (HostResolution, error) {
	if r.hostIDLabel != "" {
		if id := labels[r.hostIDLabel]; id != "" {
			return HostResolution{HostID: id, Label: r.hostIDLabel, Value: id}, nil
		}
	}
	var last HostResolution
	for _, l := range r.hostLabels {
		v := labels[l]
		if v == "" {
			continue
		}
		v = stripPort(v)
		last = HostResolution{Label: l, Value: v}
		id, err := r.find(ctx, v)
		if err != nil {
			return last, err
		}
		if id != "" {
			last.HostID = id
			return last, nil
		}
	}
	return last, nil
}

// Purge clears the host cache.
func (r *HostResolver) Purge() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.cache)
}

func (r *HostResolver) find(ctx context.Context, value string) (string, error) {
	now := r.now()
	r.mu.Lock()
	e, ok := r.cache[value]
	r.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.hostID, nil
	}

	param := &mackerel.FindHostsParam{}
	switch r.lookup {
	case HostLookupCustomIdentifier:
		param.CustomIdentifier = value
	default:
		param.Name = value
	}
	hosts, err := r.client.FindHostsContext(ctx, param)
	if err != nil {
		return "", fmt.Errorf("failed to find hosts by %s=%s: %w", r.lookup, value, err)
	}
	var id string
	ttl := r.negativeTTL
	if len(hosts) > 0 {
		if len(hosts) > 1 {
			slog.Warn("multiple hosts found, using the first one", "lookup", r.lookup, "value", value, "count", len(hosts))
		}
		id = hosts[0].ID
		ttl = r.ttl
	}
	r.mu.Lock()
	r.cache[value] = hostCacheEntry{hostID: id, expires: now.Add(ttl)}
	r.mu.Unlock()
	return id, nil
}

// stripPort removes ":port" from "host:port" (e.g. Prometheus instance label).
func stripPort(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}
