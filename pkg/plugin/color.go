package plugin

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cast"
)

func colorPercent(format string, percent float64) string {
	str := fmt.Sprintf(format, percent)
	switch {
	case percent >= 100:
		return color.New(color.Bold, color.FgRed).Sprint(str)
	case percent >= 90:
		return color.RedString(str)
	case percent >= 80:
		return color.YellowString(str)
	}
	return str
}

func colorBool(cond bool, str string) string {
	if cond {
		return color.GreenString(str)
	} else {
		return color.New(color.FgRed, color.Bold).Sprintf("%s", str)
	}
}

func redIf(cond interface{}, str string) string {
	if !reflect.ValueOf(cond).IsZero() {
		return color.RedString(str)
	}
	return str
}

func redBoldIf(cond interface{}, str string) string {
	if !reflect.ValueOf(cond).IsZero() {
		return color.New(color.FgRed, color.Bold).Sprintf("%s", str)
	}
	return str
}

func markRed(regex, s string) string {
	return markWithColor(regex, s, color.RedString)
}

func markYellow(regex, s string) string {
	return markWithColor(regex, s, color.YellowString)
}

func markGreen(regex, s string) string {
	return markWithColor(regex, s, color.GreenString)
}

func markWithColor(regex string, s string, colorStringFunc func(format string, a ...interface{}) string) string {
	re := regexp.MustCompile(regex)
	var result []string
	for _, line := range strings.Split(s, "\n") {
		for _, match := range re.FindAllString(line, -1) {
			line = strings.Replace(line, match, colorStringFunc(match), 1)
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func colorKeyword(phase string) string {
	/* covers ".status.phase", ".status.state", ".status.reason", pod QOS,
		   for various types, e.g. pod, pv, pvc, svc, ns, etc ...

		Here a generic method is used since this can be used to cover unknown CRDs that follows conventions as well.
		This also helps with maintaining the list
	    E.g.:
	    * acme.cert-manager.io/v1alpha2 Order: .status.state: valid
	    * pvc: .status.phase Bound
	*/
	switch phase {
	case "Running", "Succeeded", "Available", "Bound", "valid", "Guaranteed", "Completed", "Current", "deployed":
		return color.GreenString(phase)
	case "Pending", "Released", "Burstable", "Active", "InProgress", "superseded", "pending-install", "pending-upgrade", "pending-rollback", "uninstalling":
		return color.YellowString(phase)
	case "Failed", "Unknown", "Terminating", "Evicted", "BestEffort", "OOMKilled", "ContainerCannotRun", "Error", "NotFound", "failed", "unknown":
		return color.New(color.FgRed, color.Bold).Sprintf("%s", phase)
	default:
		return phase
	}
}

func (cfg *RenderConfig) colorAgo(kubeDate string) string {
	t, _ := time.ParseInLocation("2006-01-02T15:04:05Z", kubeDate, time.UTC)
	if cfg.Viper.GetBool("absolute-time") {
		return t.Format("2006-01-02T15:04:05Z")
	}
	duration := cfg.Now().Sub(t).Round(time.Second)
	return cfg.colorDuration(duration)
}

// colorAgoUnixNano is colorAgo for VolumeSnapshotContent's status.creationTime, which the
// external-snapshotter API represents as raw Unix nanoseconds (int64) rather than the RFC3339
// metav1.Time string every other timestamp in this codebase uses.
func (cfg *RenderConfig) colorAgoUnixNano(unixNano interface{}) string {
	t := time.Unix(0, cast.ToInt64(unixNano)).UTC()
	if cfg.Viper.GetBool("absolute-time") {
		return t.Format("2006-01-02T15:04:05Z")
	}
	duration := cfg.Now().Sub(t).Round(time.Second)
	return cfg.colorDuration(duration)
}

func (cfg *RenderConfig) colorDuration(duration time.Duration) string {
	str := cfg.DurationRound(duration.String())
	if duration < time.Minute*5 {
		return color.RedString(str)
	}
	if duration < time.Hour {
		return color.YellowString(str)
	}
	if duration < time.Hour*24 {
		return color.MagentaString(str)
	}
	return str
}
