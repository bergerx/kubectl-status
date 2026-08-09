package plugin

import (
	"fmt"
	"strings"
	"time"

	sprouttime "github.com/go-sprout/sprout/registry/time"
	"github.com/robfig/cron/v3"
)

func DefaultDurationRound() func(duration interface{}) string {
	return sprouttime.NewRegistry().DurationRound
}

// defaultStartedAfterClause renders the ", started after <duration>" suffix of the status
// summary line. Both timestamps come off the wire at 1-second resolution, so on a live cluster
// whether this clause appears at all hinges on whether the pod's creation and kubelet-acknowledge
// timestamps land in the same wall-clock second -- a coin flip e2e tests can't control. Tests
// override RenderConfig.StartedAfterClause so the clause is deterministic instead of tied to
// that real scheduling latency.
func defaultStartedAfterClause(cfg *RenderConfig) func(createdKubeDate, startedKubeDate string) string {
	return func(createdKubeDate, startedKubeDate string) string {
		created, err := time.Parse(time.RFC3339, createdKubeDate)
		if err != nil {
			return ""
		}
		started, err := time.Parse(time.RFC3339, startedKubeDate)
		if err != nil {
			return ""
		}
		duration := started.Sub(created)
		if duration <= 0 {
			return ""
		}
		return ", started after " + cfg.colorDuration(duration)
	}
}

func (cfg *RenderConfig) startedAfterClause(createdKubeDate, startedKubeDate string) string {
	return cfg.StartedAfterClause(createdKubeDate, startedKubeDate)
}

func (cfg *RenderConfig) ago(t time.Time) string {
	duration := cfg.Now().Sub(t).Round(time.Second)
	return cfg.DurationRound(duration.String())
}

func (cfg *RenderConfig) agoSuffix() string {
	if cfg.Viper.GetBool("absolute-time") {
		return ""
	}
	return " ago"
}

func (cfg *RenderConfig) forOrSince() string {
	if cfg.Viper.GetBool("absolute-time") {
		return "since"
	}
	return "for"
}

func (cfg *RenderConfig) withinLastHour(kubeDate interface{}) bool {
	s, ok := kubeDate.(string)
	if !ok || s == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return false
	}
	d := cfg.Now().Sub(t)
	return d >= 0 && d < time.Hour
}

func (cfg *RenderConfig) relativeTime(kubeDate string) string {
	if cfg.Viper.GetBool("absolute-time") {
		return ""
	}
	t, _ := time.ParseInLocation("2006-01-02T15:04:05Z", kubeDate, time.UTC)
	duration := cfg.Now().Sub(t).Round(time.Second)
	return fmt.Sprintf(" (%s ago)", cfg.colorDuration(duration))
}

// untilClause renders a " (in <duration>)" suffix for a future timestamp -- the same shape
// cronNextTime has printed for a CronJob's next scheduled run -- so any other "expires at /
// valid until" timestamp (cert validity, bootstrap-token expiration, ...) can share it instead
// of re-deriving the countdown inline. Returns "" for a non-future t or under --absolute-time,
// same as the other *Clause/relativeTime helpers.
func (cfg *RenderConfig) untilClause(t time.Time) string {
	if cfg.Viper.GetBool("absolute-time") {
		return ""
	}
	duration := t.Sub(cfg.Now()).Round(time.Second)
	if duration <= 0 {
		return ""
	}
	return fmt.Sprintf(" (in %s)", cfg.colorDuration(duration))
}

func (cfg *RenderConfig) cronNextTime(schedule string, timezone interface{}) string {
	tz, _ := timezone.(string)
	schedStr := schedule
	if !strings.Contains(schedule, "TZ") && tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			schedStr = fmt.Sprintf("TZ=%s %s", tz, schedule)
		}
	}
	sched, err := cron.ParseStandard(schedStr)
	if err != nil {
		return ""
	}
	now := cfg.Now()
	next := sched.Next(now)
	if next.IsZero() {
		return ""
	}
	nextStr := next.UTC().Format("2006-01-02T15:04:05Z")
	return nextStr + cfg.untilClause(next)
}
