package app

import (
	"context"
	"testing"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestParsePollSources(t *testing.T) {
	cfg := &config.Config{Sources: []config.SourceConfig{
		{ID: "alpha", Enabled: true},
		{ID: "beta", Enabled: true},
		{ID: "disabled", Enabled: false},
	}}

	sources, err := ParsePollSources(cfg, " beta, alpha, beta ")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].ID != "beta" || sources[1].ID != "alpha" {
		t.Fatalf("ParsePollSources() = %#v, want beta then alpha", sources)
	}

	sources, err = ParsePollSources(cfg, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].ID != "alpha" || sources[1].ID != "beta" {
		t.Fatalf("ParsePollSources(all) = %#v, want all enabled sources", sources)
	}
}

func TestParsePollSourcesRejectsInvalidSelection(t *testing.T) {
	cfg := &config.Config{Sources: []config.SourceConfig{
		{ID: "alpha", Enabled: true},
		{ID: "disabled", Enabled: false},
	}}
	for _, value := range []string{"", "unknown", "disabled", "all,alpha"} {
		if _, err := ParsePollSources(cfg, value); err == nil {
			t.Errorf("ParsePollSources(%q) unexpectedly succeeded", value)
		}
	}
}

func TestEnqueuePollsValidatesBatchBeforeConnecting(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Limits: config.LimitsConfig{MaxFeedItemsPerRun: 5}},
	}
	sources := []config.SourceConfig{{ID: "alpha", Enabled: true}}
	tests := []struct {
		name  string
		times int
		limit int
	}{
		{name: "zero times", times: 0},
		{name: "too many times", times: MaxManualPollTimes + 1},
		{name: "negative limit", times: 1, limit: -1},
		{name: "limit above source maximum", times: 1, limit: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := EnqueuePolls(context.Background(), cfg, sources, test.times, test.limit); err == nil {
				t.Fatal("EnqueuePolls() unexpectedly succeeded")
			}
		})
	}
}
