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

// resourceRef renders a "Kind/name" reference, appending " -n namespace" unless namespace equals
// callerNamespace, and gluing an optional nameSuffix (e.g. a port) onto name so it can't be
// misread as qualifying the trailing " -n namespace" instead. Backs the `{{define "resource_ref"}}`
// partial in templates/common.tmpl, which is on TEMPLATE-API.md's stable-name list -- every
// existing/external user template calling `{{template "resource_ref" (dict ...)}}` keeps working
// unchanged against that thin wrapper.
//
// apiVersionOrGroup is whatever the reference says about its API group -- a full apiVersion
// ("keyvault.azure.m.upbound.io/v1beta1"), a bare group, or nothing. A group Kubernetes doesn't
// serve itself is rendered into the Kind as "Kind.group/name": Kind alone is not a unique name
// across groups, and a Crossplane/Flux-managed "Secret" from keyvault.azure.m.upbound.io shown as
// plain "Secret/foo" reads as the core Secret it isn't. Built-in groups stay off screen (see
// isBuiltinAPIGroup) -- "Deployment.apps/foo" is noise nobody asked for.
//
// kind/name/namespace/callerNamespace/nameSuffix/apiVersionOrGroup are interface{} rather than
// string: a ref field a CRD marks required can still arrive nil from a hand-written manifest
// rendered with -f (a missing dict key resolves to an untyped nil the same way), and cast.ToString
// degrades that to "" instead of erroring the call and aborting the whole object's render.
func resourceRef(kind, name, namespace, callerNamespace, nameSuffix, apiVersionOrGroup interface{}) string {
	kindStr := displayKind(kind, apiVersionOrGroup)
	nameStr := cast.ToString(name)
	namespaceStr := cast.ToString(namespace)
	callerNamespaceStr := cast.ToString(callerNamespace)
	nameSuffixStr := cast.ToString(nameSuffix)

	var b strings.Builder
	b.WriteString(color.New(color.Bold).Sprintf("%s", color.CyanString(kindStr)))
	b.WriteString("/")
	b.WriteString(color.CyanString(nameStr))
	b.WriteString(nameSuffixStr)
	if namespaceStr != "" && (callerNamespaceStr == "" || namespaceStr != callerNamespaceStr) {
		b.WriteString(" -n ")
		if namespaceStr == "default" {
			b.WriteString(color.RedString(namespaceStr))
		} else {
			b.WriteString(color.CyanString(namespaceStr))
		}
	}
	return b.String()
}

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
