package alertmanager2mackerel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mackerelio/mackerel-client-go"
)

type mockClient struct {
	mu        sync.Mutex
	hosts     map[string]string // name or customIdentifier -> host ID
	findErr   error
	postErr   error
	findCalls []mackerel.FindHostsParam
	posted    []*mackerel.CheckReports
}

func (m *mockClient) FindHostsContext(_ context.Context, param *mackerel.FindHostsParam) ([]*mackerel.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findCalls = append(m.findCalls, *param)
	if m.findErr != nil {
		return nil, m.findErr
	}
	key := param.Name
	if param.CustomIdentifier != "" {
		key = param.CustomIdentifier
	}
	if id, ok := m.hosts[key]; ok {
		return []*mackerel.Host{{ID: id, Name: key}}, nil
	}
	return nil, nil
}

func (m *mockClient) PostCheckReportsContext(_ context.Context, reports *mackerel.CheckReports) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.postErr != nil {
		return m.postErr
	}
	m.posted = append(m.posted, reports)
	return nil
}

func newTestResolver(t *testing.T, client MackerelClient, lookup string) *HostResolver {
	t.Helper()
	r, err := NewHostResolver(client, HostResolverOptions{
		HostIDLabel: "mackerel_host_id",
		HostLabels:  []string{"instance", "host"},
		Lookup:      lookup,
		TTL:         10 * time.Minute,
		NegativeTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHostResolverResolve(t *testing.T) {
	client := &mockClient{hosts: map[string]string{"web-01": "host-web01", "db-01": "host-db01"}}
	r := newTestResolver(t, client, HostLookupName)
	ctx := context.Background()

	tests := []struct {
		name   string
		labels map[string]string
		want   HostResolution
	}{
		{"host id label", map[string]string{"mackerel_host_id": "direct", "instance": "web-01:9100"}, HostResolution{HostID: "direct", Label: "mackerel_host_id", Value: "direct"}},
		{"instance with port", map[string]string{"instance": "web-01:9100"}, HostResolution{HostID: "host-web01", Label: "instance", Value: "web-01"}},
		{"fallback to next label", map[string]string{"instance": "10.0.0.1:9100", "host": "db-01"}, HostResolution{HostID: "host-db01", Label: "host", Value: "db-01"}},
		{"not found", map[string]string{"instance": "unknown:9100"}, HostResolution{Label: "instance", Value: "unknown"}},
		{"no labels", map[string]string{}, HostResolution{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Resolve(ctx, tt.labels)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHostResolverCache(t *testing.T) {
	client := &mockClient{hosts: map[string]string{"web-01": "host-web01"}}
	r := newTestResolver(t, client, HostLookupName)
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ctx := context.Background()

	resolve := func(v string) string {
		t.Helper()
		res, err := r.Resolve(ctx, map[string]string{"instance": v})
		if err != nil {
			t.Fatal(err)
		}
		return res.HostID
	}

	resolve("web-01")
	resolve("web-01")
	resolve("unknown")
	resolve("unknown")
	if n := len(client.findCalls); n != 2 {
		t.Fatalf("find calls = %d, want 2", n)
	}

	// negative cache expires
	now = now.Add(2 * time.Minute)
	resolve("web-01")
	resolve("unknown")
	if n := len(client.findCalls); n != 3 {
		t.Fatalf("find calls = %d, want 3", n)
	}

	// positive cache expires
	now = now.Add(10 * time.Minute)
	resolve("web-01")
	if n := len(client.findCalls); n != 4 {
		t.Fatalf("find calls = %d, want 4", n)
	}

	r.Purge()
	resolve("web-01")
	if n := len(client.findCalls); n != 5 {
		t.Fatalf("find calls = %d, want 5", n)
	}
}

func TestHostResolverCustomIdentifier(t *testing.T) {
	client := &mockClient{hosts: map[string]string{"i-0123": "host-ec2"}}
	r := newTestResolver(t, client, HostLookupCustomIdentifier)
	res, err := r.Resolve(context.Background(), map[string]string{"host": "i-0123"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HostID != "host-ec2" {
		t.Errorf("got %q", res.HostID)
	}
	if client.findCalls[0].CustomIdentifier != "i-0123" || client.findCalls[0].Name != "" {
		t.Errorf("unexpected param %+v", client.findCalls[0])
	}
}

func TestHostResolverError(t *testing.T) {
	client := &mockClient{findErr: errors.New("network error")}
	r := newTestResolver(t, client, HostLookupName)
	if _, err := r.Resolve(context.Background(), map[string]string{"instance": "web-01"}); err == nil {
		t.Error("expected error")
	}
}

func TestNewHostResolverInvalidLookup(t *testing.T) {
	if _, err := NewHostResolver(&mockClient{}, HostResolverOptions{Lookup: "ip"}); err == nil {
		t.Error("expected error")
	}
}
