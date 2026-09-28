package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScreeningConfigInheritanceAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, defaults, source string
		enabled                bool
		instructions           string
		wantError              string
	}{
		{name: "omitted"},
		{name: "reject removed llm", source: `{enabled: false, llm: [one]}`, wantError: "no longer supported"},
		{name: "source enables", defaults: `{enabled: false, services: [llm.one], instructions: inherited}`, source: `{enabled: true}`, enabled: true, instructions: "inherited"},
		{name: "source disables", defaults: `{enabled: true, services: [llm.one]}`, source: `{enabled: false}`},
		{name: "clear instructions", defaults: `{enabled: true, services: [llm.one], instructions: inherited}`, source: `{instructions: ""}`, enabled: true},
		{name: "replace instructions", defaults: `{enabled: true, services: [llm.one], instructions: inherited}`, source: `{instructions: custom}`, enabled: true, instructions: "custom"},
		{name: "no generation fallback", source: `{enabled: true}`, wantError: "at least one service"},
		{name: "unknown service", source: `{enabled: true, services: [llm.missing]}`, wantError: "unknown service"},
		{name: "empty list overrides", defaults: `{enabled: false, services: [llm.one]}`, source: `{enabled: true, services: []}`, wantError: "at least one service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := minimalConfig
			if tc.defaults != "" {
				data = strings.Replace(data, "defaults:\n", "defaults:\n  screening: "+tc.defaults+"\n", 1)
			}
			if tc.source != "" {
				data = strings.Replace(data, "  - id: test\n", "  - id: test\n    screening: "+tc.source+"\n", 1)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.EffectiveScreening(cfg.Sources[0])
			if got.IsEnabled() != tc.enabled {
				t.Fatalf("enabled=%v", got.IsEnabled())
			}
			instructions := ""
			if got.Instructions != nil {
				instructions = *got.Instructions
			}
			if instructions != tc.instructions {
				t.Fatalf("instructions=%q", instructions)
			}
		})
	}
}

func TestScreeningChains(t *testing.T) {
	enabled := true
	threshold := 0.8
	cfg := &Config{Services: ServicesConfig{Jev: LLMService{BaseURL: "https://example.com/v1", Model: "jev-test", Timeout: "1s"}, LLM: map[string]LLMService{"one": {Type: "openai_compatible", BaseURL: "https://example.com/v1", Model: "test", Timeout: "1s"}}}, Defaults: DefaultsConfig{Screening: ScreeningConfig{Enabled: &enabled, Services: []string{"jev", "llm.one"}, Jev: ScreeningJevConfig{SkipThreshold: &threshold}}}}
	for _, tc := range []struct {
		name     string
		services []string
		want     string
	}{
		{"chain", []string{"jev", "llm.one"}, ""},
		{"unknown", []string{"one"}, "unknown service"},
		{"empty", []string{}, "at least one"},
		{"duplicate", []string{"jev", "jev"}, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := cfg.Defaults.Screening
			v.Services = tc.services
			err := cfg.validateScreening("test", v)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	got := cfg.EffectiveScreening(SourceConfig{Screening: &ScreeningConfig{Services: []string{"llm.one"}}})
	if len(got.Services) != 1 || got.Services[0] != "llm.one" || got.Jev.Threshold() != 0.8 {
		t.Fatalf("override=%+v", got)
	}
	for _, v := range []float64{0, -0.1, 1.1} {
		bad := cfg.Defaults.Screening
		bad.Jev.SkipThreshold = &v
		if cfg.validateScreening("test", bad) == nil {
			t.Fatalf("accepted threshold %v", v)
		}
	}
	if (ScreeningJevConfig{}).Threshold() != 0.7 {
		t.Fatal("incorrect default")
	}
}
