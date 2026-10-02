// Package fakemackerel provides a fake Mackerel API server for testing.
package fakemackerel

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
)

// Host is a host registered in the fake server.
type Host struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CustomIdentifier string `json:"customIdentifier,omitempty"`
}

// Report is a check report received by the fake server.
type Report struct {
	Source struct {
		Type   string `json:"type"`
		HostID string `json:"hostId"`
	} `json:"source"`
	Name                 string `json:"name"`
	Status               string `json:"status"`
	Message              string `json:"message"`
	OccurredAt           int64  `json:"occurredAt"`
	NotificationInterval uint   `json:"notificationInterval,omitempty"`
	MaxCheckAttempts     uint   `json:"maxCheckAttempts,omitempty"`
}

// Server is a fake Mackerel API server.
type Server struct {
	*httptest.Server
	apiKey string

	mu           sync.Mutex
	hosts        []Host
	reports      []Report
	hostLookups  []string // raw query strings of GET /api/v0/hosts
	postFailures []int    // status codes returned for the next POST requests
	postRequests int
	unauthorized int
	onReport     func(Report)
}

// New starts a fake Mackerel API server which requires the API key (any key is accepted if apiKey is empty).
// If addr is empty, a random local port is used.
// GET /_reports returns the received check reports.
func New(apiKey, addr string, hosts ...Host) (*Server, error) {
	s := &Server{apiKey: apiKey, hosts: hosts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v0/hosts", s.findHosts)
	mux.HandleFunc("POST /api/v0/monitoring/checks/report", s.postReports)
	mux.HandleFunc("GET /_reports", func(w http.ResponseWriter, _ *http.Request) {
		reports := s.Reports()
		if reports == nil {
			reports = []Report{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
	})
	ts := httptest.NewUnstartedServer(s.auth(mux))
	if addr != "" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		ts.Listener.Close()
		ts.Listener = l
	}
	ts.Start()
	s.Server = ts
	return s, nil
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey != "" && r.URL.Path != "/_reports" && r.Header.Get("X-Api-Key") != s.apiKey {
			s.mu.Lock()
			s.unauthorized++
			s.mu.Unlock()
			writeError(w, http.StatusForbidden, "invalid api key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) findHosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	s.hostLookups = append(s.hostLookups, r.URL.RawQuery)
	found := []Host{}
	for _, h := range s.hosts {
		if name := q.Get("name"); name != "" && h.Name != name {
			continue
		}
		if cid := q.Get("customIdentifier"); cid != "" && h.CustomIdentifier != cid {
			continue
		}
		found = append(found, h)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"hosts": found})
}

func (s *Server) postReports(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.postRequests++
	if len(s.postFailures) > 0 {
		code := s.postFailures[0]
		s.postFailures = s.postFailures[1:]
		writeError(w, code, http.StatusText(code))
		return
	}
	var body struct {
		Reports []Report `json:"reports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.reports = append(s.reports, body.Reports...)
	if s.onReport != nil {
		for _, rep := range body.Reports {
			s.onReport(rep)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// OnReport sets a function called for each received check report.
func (s *Server) OnReport(f func(Report)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onReport = f
}

// FailNextPosts makes the next POST requests fail with the status codes.
func (s *Server) FailNextPosts(codes ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.postFailures = append(s.postFailures, codes...)
}

// Reports returns the received check reports.
func (s *Server) Reports() []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reports)
}

// HostLookups returns the raw queries of host lookups.
func (s *Server) HostLookups() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hostLookups)
}

// PostRequests returns the number of POST requests including failed ones.
func (s *Server) PostRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.postRequests
}

// Unauthorized returns the number of requests with an invalid API key.
func (s *Server) Unauthorized() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unauthorized
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
