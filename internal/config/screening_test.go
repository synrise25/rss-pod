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
		{name: "source enables", defaults: `{enabled: false, llm: [one], instructions: inherited}`, source: `{enabled: true}`, enabled: true, instructions: "inherited"},
		{name: "source disables", defaults: `{enabled: true, llm: [one]}`, source: `{enabled: false}`},
		{name: "clear instructions", defaults: `{enabled: true, llm: [one], instructions: inherited}`, source: `{instructions: ""}`, enabled: true},
		{name: "replace instructions", defaults: `{enabled: true, llm: [one], instructions: inherited}`, source: `{instructions: custom}`, enabled: true, instructions: "custom"},
		{name: "no generation fallback", source: `{enabled: true}`, wantError: "at least one service"},
		{name: "unknown service", source: `{enabled: true, llm: [missing]}`, wantError: "unknown service"},
		{name: "empty list overrides", defaults: `{enabled: false, llm: [one]}`, source: `{enabled: true, llm: []}`, wantError: "at least one service"},
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
