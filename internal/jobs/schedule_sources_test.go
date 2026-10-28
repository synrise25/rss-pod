package jobs

import (
	"testing"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestDueSourcePollsCatchUp(t *testing.T) {
	for _, tc := range []struct {
		name, timezone, cron, lastChecked, now string
		want                                   []string
	}{
		{
			name: "missed minute", timezone: "Asia/Shanghai", cron: "50 22 * * *",
			lastChecked: "2026-10-27T22:49:59+08:00", now: "2026-10-27T22:51:02+08:00",
			want: []string{"2026-10-27T22:50:00+08:00"},
		},
		{
			name: "multiple occurrences and inclusive end", timezone: "UTC", cron: "*/10 * * * *",
			lastChecked: "2026-10-27T22:10:00Z", now: "2026-10-27T22:40:02Z",
			want: []string{"2026-10-27T22:20:00Z", "2026-10-27T22:30:00Z", "2026-10-27T22:40:00Z"},
		},
		{
			name: "cross midnight preserves yesterday", timezone: "Asia/Shanghai", cron: "59 23 * * *",
			lastChecked: "2026-10-27T23:58:00+08:00", now: "2026-10-28T00:01:00+08:00",
			want: []string{"2026-10-27T23:59:00+08:00"},
		},
		{
			name: "repeated minute", timezone: "UTC", cron: "* * * * *",
			lastChecked: "2026-10-27T22:50:00Z", now: "2026-10-27T22:50:59Z",
		},
		{
			name: "clock moves backwards", timezone: "UTC", cron: "* * * * *",
			lastChecked: "2026-10-27T22:51:00Z", now: "2026-10-27T22:50:00Z",
		},
		{
			name: "long outage excludes old occurrences", timezone: "UTC", cron: "50 * * * *",
			lastChecked: "2026-10-25T22:00:00Z", now: "2026-10-27T22:51:00Z",
			want: []string{"2026-10-27T22:50:00Z"},
		},
		{
			name: "no possible cron date", timezone: "UTC", cron: "0 0 31 2 *",
			lastChecked: "2026-10-27T22:49:00Z", now: "2026-10-27T22:51:00Z",
		},
		{
			name: "DST repeated hour", timezone: "America/New_York", cron: "30 1 * * *",
			lastChecked: "2026-11-01T05:45:00Z", now: "2026-11-01T06:31:00Z",
			want: []string{"2026-11-01T06:30:00Z"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Defaults: config.DefaultsConfig{Schedule: config.ScheduleConfig{Timezone: tc.timezone}},
				Sources:  []config.SourceConfig{{ID: "test", Enabled: true, Schedule: config.ScheduleConfig{Cron: tc.cron}}},
			}
			got, err := dueSourcePolls(cfg, parseScheduleTime(t, tc.lastChecked), parseScheduleTime(t, tc.now))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("polls=%+v, want %v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i].SourceID != "test" || !got[i].ScheduledFor.Equal(parseScheduleTime(t, want)) {
					t.Errorf("poll[%d]=%+v, want %s", i, got[i], want)
				}
			}
		})
	}
}

func TestDueSourcePollsBoundsFrequentSchedules(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Schedule: config.ScheduleConfig{Timezone: "UTC"}},
		Sources:  []config.SourceConfig{{ID: "test", Enabled: true, Schedule: config.ScheduleConfig{Cron: "* * * * *"}}},
	}
	now := parseScheduleTime(t, "2026-10-28T00:00:00Z")
	got, err := dueSourcePolls(cfg, now.Add(-48*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 60 || !got[0].ScheduledFor.Equal(now.Add(-59*time.Minute)) || !got[59].ScheduledFor.Equal(now) {
		t.Fatalf("catch-up window polls=%+v, want last 60 minutes", got)
	}
	cfg.Defaults.Schedule.Timezone = "invalid/timezone"
	if _, err := dueSourcePolls(cfg, now.Add(-time.Minute), now); err == nil {
		t.Fatal("accepted invalid timezone")
	}
}

func parseScheduleTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
