package plugin

import (
	"text/template"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/viper"
	"k8s.io/klog/v2"
)

// RenderConfig carries the per-invocation configuration and time/duration hooks that template
// functions read, so that concurrent renders (e.g. parallel e2e subtests) don't share mutable
// process-global state. Viper is a *viper.Viper instance owned by this invocation (not the
// package-level global singleton); Now/DurationRound/StartedAfterClause default to the real
// implementations and are only overridden by tests.
type RenderConfig struct {
	Viper              *viper.Viper
	Now                func() time.Time
	DurationRound      func(duration interface{}) string
	StartedAfterClause func(createdKubeDate, startedKubeDate string) string
	// StatefulSetRollbackTrapThreshold is how long a StatefulSet-owned Pod must have been unready
	// before StatefulSetRollbackTrap treats it as evidence of the kubernetes/kubernetes#67250
	// "stuck rollback" trap, rather than ordinary image-pull/startup latency. Deliberately
	// compared against real wall-clock time (not Now, which tests freeze to a fixed date for
	// deterministic rendering) and left overridable here so e2e tests don't need to wait out the
	// real default.
	StatefulSetRollbackTrapThreshold time.Duration
}

// NewRenderConfig builds a RenderConfig backed by v, with the real Now/DurationRound/
// StartedAfterClause implementations.
func NewRenderConfig(v *viper.Viper) *RenderConfig {
	cfg := &RenderConfig{
		Viper:                            v,
		Now:                              time.Now,
		DurationRound:                    DefaultDurationRound(),
		StatefulSetRollbackTrapThreshold: 10 * time.Minute,
	}
	cfg.StartedAfterClause = defaultStartedAfterClause(cfg)
	return cfg
}

// testHackNow is the fixed timestamp ApplyTestHack pins RenderConfig.Now to, so relative-duration
// output (e.g. "Ready: 15h") is deterministic instead of drifting with wall-clock time. Test
// fixtures under tests/artifacts/ are dated relative to this.
var testHackNow = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

// ApplyTestHack overrides cfg's time-sensitive hooks for deterministic output: a fixed Now, a
// fixed "1m" DurationRound, and a fixed "started after" clause. Real creation and kubelet-ack
// timestamps only carry 1-second resolution over the wire, so whether the "started after" clause
// appears is otherwise a coin flip; this forces it present whenever Status.startTime is set.
//
// Both the "--test-hack" CLI flag (cmd/main.go, used by `make update-artifacts`/`make
// new-artifact`) and the e2e test suite (cmd/e2e_*_test.go) call this, so the artifacts generated
// on disk and the output the tests compare against can never drift apart.
func ApplyTestHack(cfg *RenderConfig) {
	cfg.Now = func() time.Time { return testHackNow }
	cfg.DurationRound = func(_ interface{}) string { return "1m" }
	cfg.StartedAfterClause = func(_, _ string) string { return ", started after 1m" }
}

func (cfg *RenderConfig) funcMap() template.FuncMap {
	return template.FuncMap{
		"green":                           color.GreenString,
		"yellow":                          color.YellowString,
		"red":                             color.RedString,
		"cyan":                            color.CyanString,
		"blue":                            color.BlueString,
		"bold":                            color.New(color.Bold).SprintfFunc(),
		"colorAgo":                        cfg.colorAgo,
		"colorAgoUnixNano":                cfg.colorAgoUnixNano,
		"colorDuration":                   cfg.colorDuration,
		"startedAfterClause":              cfg.startedAfterClause,
		"colorBool":                       colorBool,
		"colorKeyword":                    colorKeyword,
		"markRed":                         markRed,
		"markYellow":                      markYellow,
		"markGreen":                       markGreen,
		"redIf":                           redIf,
		"redBoldIf":                       redBoldIf,
		"signalName":                      signalName,
		"isStatusConditionHealthy":        isStatusConditionHealthy,
		"quantityToFloat64":               quantityToFloat64,
		"quantityToInt64":                 quantityToInt64,
		"percent":                         percent,
		"colorPercent":                    colorPercent,
		"resourceRef":                     resourceRef,
		"evictionHeadroom":                evictionHeadroom,
		"evictionAnnotation":              evictionAnnotation,
		"quotaRolloutHeadroom":            quotaRolloutHeadroom,
		"humanizeSI":                      humanizeSI,
		"humanizeSIPair":                  humanizeSIPair,
		"getMatchingItemInMapList":        getMatchingItemInMapList,
		"sortMapListByKeysValue":          sortMapListByKeysValue,
		"sortMapListByFloatKeysValueDesc": sortMapListByFloatKeysValueDesc,
		"fieldsV1Paths":                   fieldsV1Paths,
		"sortByRevisionAnnotation":        sortByRevisionAnnotation,
		"sortByRevisionField":             sortByRevisionField,
		"addFloat64":                      addFloat64,
		"subFloat64":                      subFloat64,
		"divFloat64":                      divFloat64,
		"ip":                              cfg.ip,
		"agoSuffix":                       cfg.agoSuffix,
		"forOrSince":                      cfg.forOrSince,
		"relativeTime":                    cfg.relativeTime,
		"untilClause":                     cfg.untilClause,
		"labelSelector":                   labelSelector,
		"taintsNotToleratedByPod":         taintsNotToleratedByPod,
		"nodeCloudProvider":               nodeCloudProvider,
		"formatNodeSelector":              formatNodeSelector,
		"formatNodeSelectorTerms":         formatNodeSelectorTerms,
		"podHardConstraintRequirements":   podHardConstraintRequirements,
		"karpenterUnsatisfiableKeys":      karpenterUnsatisfiableKeys,
		"karpenterDisqualifyingKey":       karpenterDisqualifyingKey,
		"networkPolicyPolicyTypes":        networkPolicyPolicyTypes,
		"calicoPolicyTypes":               calicoPolicyTypes,
		"ciliumPolicyDirections":          ciliumPolicyDirectionsForTemplate,
		"cronNextTime":                    cfg.cronNextTime,
		"withinLastHour":                  cfg.withinLastHour,
		"parseTLSSecretCertificate":       cfg.parseTLSSecretCertificate,
		"qualifyKind":                     qualifyKind,
		"hostnameIntersections":           hostnameIntersections,
		"istioHost":                       istioHost,
		"certificatesInSecret":            cfg.certificatesInSecret,
		"certificatesInConfigMap":         cfg.certificatesInConfigMap,
		"certificateInCSR":                cfg.certificateInCSR,
		"certificateRequestInCSR":         certificateRequestInCSR,
		"parseDockerConfigSecret":         parseDockerConfigSecret,
		"parseBasicAuthSecret":            parseBasicAuthSecret,
		"parseSSHAuthSecret":              parseSSHAuthSecret,
		"parseServiceAccountTokenSecret":  parseServiceAccountTokenSecret,
		"parseBootstrapTokenSecret":       cfg.parseBootstrapTokenSecret,
		"parseHelmReleaseSecret":          parseHelmReleaseSecret,
		"helmReleaseManifestResources":    helmReleaseManifestResources,
		"secretDataKeys":                  secretDataKeys,
		"crossplaneManagedResourceDrift":  crossplaneManagedResourceDrift,
		"crossplaneDriftLabel":            crossplaneDriftLabel,
		"renderGroupedTable":              renderGroupedTable,
	}
}

func (r RenderableObject) Include(templateName string, data interface{}) (string, error) {
	klog.V(5).InfoS("Include", "r", r, "templateName", templateName, "data", data)
	return r.renderTemplate(templateName, data)
}

func (r RenderableObject) IncludeRenderableObject(obj RenderableObject) (output string) {
	klog.V(5).InfoS("called IncludeRenderableObject", "r", r, "obj", obj)
	renderString, _ := obj.renderString()
	return renderString
}
