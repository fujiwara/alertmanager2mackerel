package alertmanager2mackerel

import "testing"

func TestCLINewHandlerEmptyAPIKey(t *testing.T) {
	cli := &CLI{MackerelAPIBase: "https://api.mackerelio.com/"}
	if _, err := cli.NewHandler(); err == nil {
		t.Error("expected error for empty API key")
	}
}
