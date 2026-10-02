// Command fakemackerel runs a fake Mackerel API server for trying alertmanager2mackerel locally.
//
// It accepts check reports and logs them. Received reports are also available at GET /_reports.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fujiwara/alertmanager2mackerel/internal/fakemackerel"
)

func main() {
	addr := flag.String("listen", ":8080", "listen address")
	hosts := flag.String("hosts", "web-01=HOST-WEB01,db-01=HOST-DB01", "hosts as name=id[:customIdentifier], comma separated")
	apiKey := flag.String("api-key", "", "API key required (empty: accept any key)")
	flag.Parse()

	var hs []fakemackerel.Host
	for entry := range strings.SplitSeq(*hosts, ",") {
		name, rest, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "invalid host %q\n", entry)
			os.Exit(2)
		}
		id, cid, _ := strings.Cut(rest, ":")
		hs = append(hs, fakemackerel.Host{ID: id, Name: name, CustomIdentifier: cid})
	}

	s, err := fakemackerel.New(*apiKey, *addr, hs...)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	defer s.Close()
	s.OnReport(func(r fakemackerel.Report) {
		slog.Info("check report", "host_id", r.Source.HostID, "name", r.Name, "status", r.Status, "message", r.Message, "occurred_at", r.OccurredAt)
	})
	slog.Info("fake Mackerel API is running", "url", s.URL, "hosts", hs)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}
