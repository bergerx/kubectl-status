package plugin

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	sprouttime "github.com/go-sprout/sprout/registry/time"
	"github.com/robfig/cron/v3"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"golang.org/x/crypto/ssh"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"github.com/bergerx/kubectl-status/pkg/plugin/calicoselector"
	"github.com/bergerx/kubectl-status/pkg/plugin/crossplanedrift"
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
}

// NewRenderConfig builds a RenderConfig backed by v, with the real Now/DurationRound/
// StartedAfterClause implementations.
func NewRenderConfig(v *viper.Viper) *RenderConfig {
	cfg := &RenderConfig{
		Viper:         v,
		Now:           time.Now,
		DurationRound: DefaultDurationRound(),
	}
	cfg.StartedAfterClause = defaultStartedAfterClause(cfg)
	return cfg
}

func DefaultDurationRound() func(duration interface{}) string {
	return sprouttime.NewRegistry().DurationRound
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
// new-artifact`) and the e2e test suite (cmd/main_test.go) call this, so the artifacts generated
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
		"networkPolicyPolicyTypes":        networkPolicyPolicyTypes,
		"calicoPolicyTypes":               calicoPolicyTypes,
		"ciliumPolicyDirections":          ciliumPolicyDirectionsForTemplate,
		"cronNextTime":                    cfg.cronNextTime,
		"withinLastHour":                  cfg.withinLastHour,
		"parseTLSSecretCertificate":       cfg.parseTLSSecretCertificate,
		"certificatesInSecret":            cfg.certificatesInSecret,
		"certificatesInConfigMap":         cfg.certificatesInConfigMap,
		"certificateInCSR":                cfg.certificateInCSR,
		"certificateRequestInCSR":         certificateRequestInCSR,
		"parseDockerConfigSecret":         parseDockerConfigSecret,
		"parseBasicAuthSecret":            parseBasicAuthSecret,
		"parseSSHAuthSecret":              parseSSHAuthSecret,
		"parseServiceAccountTokenSecret":  parseServiceAccountTokenSecret,
		"parseBootstrapTokenSecret":       cfg.parseBootstrapTokenSecret,
		"secretDataKeys":                  secretDataKeys,
		"crossplaneManagedResourceDrift":  crossplaneManagedResourceDrift,
		"crossplaneDriftLabel":            crossplaneDriftLabel,
	}
}

func (cfg *RenderConfig) ip(ip string) string {
	if cfg.Viper.GetBool("test-hack") {
		return "1.1.1.1"
	}
	return ip
}

func addFloat64(i ...interface{}) float64 {
	var a float64 = 0
	for _, b := range i {
		a += cast.ToFloat64(b)
	}
	return a
}

func subFloat64(a, b float64) float64 {
	return b - a
}

func divFloat64(a, b float64) float64 {
	return b / a
}

func humanizeSI(unit string, input float64) string {
	return strings.Replace(humanize.SIWithDigits(input, 1, unit), " ", "", -1)
}

// humanizeSIPair renders two related values (e.g. allocatable/capacity) under a single shared SI
// unit, scaled to the larger of the two, e.g. humanizeSIPair("B", 32.8e9, 33.6e9) -> "32.8/33.6GB".
func humanizeSIPair(unit string, a, b float64) string {
	scaledB, prefix := humanize.ComputeSI(b)
	scale := 1.0
	if scaledB != 0 {
		scale = b / scaledB
	}
	return fmt.Sprintf("%s/%s%s", humanize.FtoaWithDigits(a/scale, 1), humanize.FtoaWithDigits(scaledB, 1), prefix+unit)
}

func quantityToFloat64(str string) float64 {
	quantity, _ := resource2.ParseQuantity(str)
	return float64(quantity.MilliValue()) / 1000
}

func quantityToInt64(str string) int64 {
	quantity, _ := resource2.ParseQuantity(str)
	return quantity.Value()
}

func percent(x, y float64) float64 {
	return x / y * 100
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

// getMatchingItemInMapList checks if the provided searchFor map is a subset of an item in the given mapList.
// Returns the first matching item.
//
// mapList parameter should actually be a "[]map[string]interface{}" but due to unstructured json serialisation
// we need to use "[]interface{}" and cast it inside.
//
// searchFor parameter should actually be a "map[string]string" but due to unstructured json serialisation
// we need to use "map[string]interface{}" and cast the value to string inside.
func getMatchingItemInMapList(searchFor map[string]interface{}, mapList []interface{}) (item map[string]interface{}) {
	for _, untypedMapListItem := range mapList {
		typedMapListItem := untypedMapListItem.(map[string]interface{})
		if hasMapListAMatchingItem(searchFor, typedMapListItem) {
			klog.V(5).InfoS("getMatchingItemInMapList found a matching item", "typedMapListItem", typedMapListItem)
			return typedMapListItem
		}
	}
	klog.V(5).InfoS("getMatchingItemInMapList couldn't find any matching item", "searchFor", searchFor, "typedMapListItem", mapList)
	return
}

func hasMapListAMatchingItem(searchFor map[string]interface{}, typedMapListItem map[string]interface{}) bool {
	klog.V(5).InfoS("hasMapListAMatchingItem will search", "searchFor", searchFor, "typedMapListItem", typedMapListItem)
	if len(searchFor) == 0 {
		return false
	}
	for searchKey, searchValue := range searchFor {
		if searchValue == nil {
			continue
		}
		if strings.Contains(searchKey, ".") {
			splitSearchKey := strings.SplitN(searchKey, ".", 2)
			outerKey := splitSearchKey[0]
			innerMapListItem, exists := typedMapListItem[outerKey]
			if !exists {
				return false
			}
			innerTypedMapListItem, ok := innerMapListItem.(map[string]interface{})
			if !ok {
				return false
			}
			innerKey := splitSearchKey[1]
			innerSearchFor := map[string]interface{}{innerKey: searchValue}
			if !hasMapListAMatchingItem(innerSearchFor, innerTypedMapListItem) {
				return false
			}
			continue
		}
		mapListItem, exists := typedMapListItem[searchKey]
		if !exists || mapListItem == nil {
			return false
		}
		mapListItemValue, ok := mapListItem.(string)
		if !ok {
			return false
		}
		searchForValue, ok := searchValue.(string)
		if !ok {
			return false
		}
		if mapListItemValue != searchForValue {
			return false
		}
	}
	return true
}

// sortMapListByKeysValue returns a sorted copy of mapList based on the provided key's value.
//
// mapList parameter should actually be a "[]map[string]interface{}" but due to unstructured json serialisation
// we need to use "[]interface{}" and cast it inside.
func sortMapListByKeysValue(key string, mapList []interface{}) (result []interface{}) {
	result = append(result, mapList...)
	sort.SliceStable(result, func(i, j int) bool {
		var typedMapListItemI, typedMapListItemJ string
		if mapI, ok := result[i].(map[string]interface{}); ok {
			typedMapListItemI, _ = mapI[key].(string)
		}
		if mapJ, ok := result[j].(map[string]interface{}); ok {
			typedMapListItemJ, _ = mapJ[key].(string)
		}
		return typedMapListItemI < typedMapListItemJ
	})
	return
}

// sortMapListByFloatKeysValueDesc returns a sorted copy of mapList in descending order of the
// given key's float64 value, e.g. ranking a node's pods by measured resource usage without a
// second apiserver round trip: callers accumulate the usage as a float64 while they already have
// the metrics at hand, and only need the ordering applied once, at render time.
func sortMapListByFloatKeysValueDesc(key string, mapList []interface{}) (result []interface{}) {
	result = append(result, mapList...)
	sort.SliceStable(result, func(i, j int) bool {
		var typedMapListItemI, typedMapListItemJ float64
		if mapI, ok := result[i].(map[string]interface{}); ok {
			typedMapListItemI, _ = mapI[key].(float64)
		}
		if mapJ, ok := result[j].(map[string]interface{}); ok {
			typedMapListItemJ, _ = mapJ[key].(float64)
		}
		return typedMapListItemI > typedMapListItemJ
	})
	return
}

// fieldsV1Paths reduces a managedFields entry's FieldsV1 tree (as decoded from
// JSON, with nested keys prefixed "f:") to the deepest common path per
// top-level branch: it descends into a branch while every node along the way
// has exactly one "f:"-prefixed child, and reports that node's path once it
// hits a fork (or a leaf), so touching only spec.template yields "spec.template"
// but touching both status.conditions and status.phase yields just "status".
func fieldsV1Paths(fieldsV1 map[string]interface{}) []string {
	var paths []string
	for key, value := range fieldsV1 {
		if !strings.HasPrefix(key, "f:") {
			continue
		}
		segments := fieldsV1DeepestPath([]string{strings.TrimPrefix(key, "f:")}, value)
		paths = append(paths, joinFieldsV1Segments(segments))
	}
	sort.Strings(paths)
	return paths
}

// fieldsV1DeepestPath descends into a branch of the FieldsV1 tree while every
// node along the way has exactly one "f:"-prefixed child, returning the
// segments collected so far once it hits a fork or a leaf. This means
// touching only spec.template yields ["spec","template"], but touching both
// status.conditions and status.phase yields just ["status"]. A single owned
// label/annotation (e.g. metadata.labels.app) descends the same way, since
// its key is just another "f:"-prefixed child.
func fieldsV1DeepestPath(segments []string, value interface{}) []string {
	node, ok := value.(map[string]interface{})
	if !ok {
		return segments
	}
	var childKey string
	for key := range node {
		if !strings.HasPrefix(key, "f:") {
			continue
		}
		if childKey != "" {
			// more than one field child at this level: this is as deep as we can go
			return segments
		}
		childKey = key
	}
	if childKey == "" {
		return segments
	}
	return fieldsV1DeepestPath(append(segments, strings.TrimPrefix(childKey, "f:")), node[childKey])
}

// joinFieldsV1Segments joins path segments with ".", quoting any segment that
// itself contains a "." (e.g. the annotation key "deployment.kubernetes.io/
// revision") so it isn't misread as further nesting.
func joinFieldsV1Segments(segments []string) string {
	quoted := make([]string, len(segments))
	for i, segment := range segments {
		if strings.Contains(segment, ".") {
			quoted[i] = strconv.Quote(segment)
		} else {
			quoted[i] = segment
		}
	}
	return strings.Join(quoted, ".")
}

// sortByRevisionAnnotation returns a sorted copy of objs (RenderableObject ReplicaSets, passed as
// []interface{} since that's what the "list"/"append" template builtins produce) ordered by their
// "deployment.kubernetes.io/revision" annotation, ascending. Unlike creationTimestamp (which only
// has second resolution, so ReplicaSets created within the same rollout can tie), the revision
// annotation is a reliable total order for a Deployment's ReplicaSets.
func sortByRevisionAnnotation(objs []interface{}) []interface{} {
	result := append([]interface{}{}, objs...)
	sort.SliceStable(result, func(i, j int) bool {
		return revisionAnnotationInt(result[i]) < revisionAnnotationInt(result[j])
	})
	return result
}

func revisionAnnotationInt(obj interface{}) int {
	r, ok := obj.(RenderableObject)
	if !ok {
		return 0
	}
	v, _ := r.Annotations()["deployment.kubernetes.io/revision"].(string)
	n, _ := strconv.Atoi(v)
	return n
}

// sortByRevisionField returns a sorted copy of objs (ControllerRevisions) ordered by their
// numeric top-level "revision" field, ascending, for the same tie-breaking reason as
// sortByRevisionAnnotation.
func sortByRevisionField(objs []interface{}) []interface{} {
	result := append([]interface{}{}, objs...)
	sort.SliceStable(result, func(i, j int) bool {
		return revisionFieldInt(result[i]) < revisionFieldInt(result[j])
	})
	return result
}

func revisionFieldInt(obj interface{}) int {
	r, ok := obj.(RenderableObject)
	if !ok {
		return 0
	}
	n, _ := cast.ToIntE(r.Object["revision"])
	return n
}

var (
	userAbnormalTrueConditionTypesOnce sync.Once
	userAbnormalTrueConditionTypes     userAbnormalTrueConditionTypeMatcher
)

// userAbnormalTrueConditionTypeMatcher holds condition types loaded from the user provided
// abnormal-true-condition-types file, split by match kind.
type userAbnormalTrueConditionTypeMatcher struct {
	exact    map[string]bool
	prefixes []string // from lines like "Unhealthy*"
	suffixes []string // from lines like "*Problematic"
}

func (m userAbnormalTrueConditionTypeMatcher) matches(conditionType string) bool {
	if m.exact[conditionType] {
		return true
	}
	for _, prefix := range m.prefixes {
		if strings.HasPrefix(conditionType, prefix) {
			return true
		}
	}
	for _, suffix := range m.suffixes {
		if strings.HasSuffix(conditionType, suffix) {
			return true
		}
	}
	return false
}

// userAbnormalTrueConditionTypeMatchers loads condition types from
// ~/.kubectl-status/abnormal-true-condition-types, one per line, so users can extend the
// hardcoded list of condition types below without recompiling. Blank lines and lines starting
// with "#" are ignored. A line may be an exact condition type, a suffix pattern like
// "*Problematic", or a prefix pattern like "Unhealthy*". Read once and cached for the lifetime
// of the process.
func userAbnormalTrueConditionTypeMatchers() userAbnormalTrueConditionTypeMatcher {
	userAbnormalTrueConditionTypesOnce.Do(func() {
		userAbnormalTrueConditionTypes = userAbnormalTrueConditionTypeMatcher{exact: map[string]bool{}}
		homeDir, err := os.UserHomeDir()
		if err != nil {
			klog.V(3).ErrorS(err, "error getting user home dir, ignoring")
			return
		}
		path := filepath.Join(homeDir, ".kubectl-status", "abnormal-true-condition-types")
		data, err := os.ReadFile(path)
		if err != nil {
			klog.V(5).ErrorS(err, "error reading user provided abnormal-true condition types file, ignoring", "path", path)
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			switch {
			case line == "" || strings.HasPrefix(line, "#"):
				continue
			case strings.HasPrefix(line, "*"):
				userAbnormalTrueConditionTypes.suffixes = append(userAbnormalTrueConditionTypes.suffixes, strings.TrimPrefix(line, "*"))
			case strings.HasSuffix(line, "*"):
				userAbnormalTrueConditionTypes.prefixes = append(userAbnormalTrueConditionTypes.prefixes, strings.TrimSuffix(line, "*"))
			default:
				userAbnormalTrueConditionTypes.exact[line] = true
			}
		}
	})
	return userAbnormalTrueConditionTypes
}

func isStatusConditionHealthy(condition map[string]interface{}) bool {
	switch {
	/*
		From https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties:

		> Condition types should indicate state in the "abnormal-true" polarity. For example, if the condition indicates
		> when a policy is invalid, the "is valid" case is probably the norm, so the condition should be called
		> "Invalid".

		But apparently this is not common among most resources, so we have the list of cases that matches the expected
		behaviour rather than the exceptions.
	*/
	case strings.HasSuffix(fmt.Sprint(condition["type"]), "Pressure"), // Node Pressure conditions
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Unavailable"), // Node NetworkUnavailable condition
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Failure"),     // ReplicaSet ReplicaFailure: condition
		strings.HasPrefix(fmt.Sprint(condition["type"]), "Non"),         // CRD NonStructuralSchema condition
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Problem"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Error"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Errors"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Hung"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Missing"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Flapping"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Unhealthy"),
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Failed"), // Failed Jobs has this condition
		strings.HasSuffix(fmt.Sprint(condition["type"]), "Warning"),
		strings.HasPrefix(fmt.Sprint(condition["type"]), "Corrupt"),

		// Conditions from "Node Problem Detector"
		condition["type"] == "DockerContainerStartupFailure",
		condition["type"] == "FilesystemIsReadOnly",
		condition["type"] == "KernelDeadlock",
		condition["type"] == "KernelOops",
		condition["type"] == "OOMKilling",
		condition["type"] == "ReadonlyFilesystem",
		condition["type"] == "UnregisterNetDevice",
		condition["type"] == "FrequentDockerRestart",
		condition["type"] == "FrequentContainerdRestart",
		condition["type"] == "FrequentKubeletRestart",
		condition["type"] == "RebootScheduled",
		condition["type"] == "TerminateScheduled",
		condition["type"] == "RedeployScheduled",
		condition["type"] == "PreemptScheduled",
		condition["type"] == "FreezeScheduled",
		condition["type"] == "FrequentUnregisterNetDevice",
		condition["type"] == "VMEventScheduled",
		condition["type"] == "NVLinkStatusInactive",
		condition["type"] == "KernelDeadLock", // legacy mis-capitalized variant seen in some NPD configs
		condition["type"] == "OutOfDisk",      // deprecated legacy Node condition, same polarity as DiskPressure

		// User provided additions, see ~/.kubectl-status/abnormal-true-condition-types
		userAbnormalTrueConditionTypeMatchers().matches(fmt.Sprint(condition["type"])):
		switch condition["status"] {
		case "False":
			return true
		case "True", "Unknown":
			return false
		default:
			// not likely to ever happen, but just in case
			return false
		}
	default:
		switch condition["status"] {
		case "True":
			return true
		case "False", "Unknown":
			return false
		default:
			return false
		}
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
	case "Running", "Succeeded", "Available", "Bound", "valid", "Guaranteed", "Completed", "Current":
		return color.GreenString(phase)
	case "Pending", "Released", "Burstable", "Active", "InProgress":
		return color.YellowString(phase)
	case "Failed", "Unknown", "Terminating", "Evicted", "BestEffort", "OOMKilled", "ContainerCannotRun", "Error", "NotFound":
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

func (r RenderableObject) Include(templateName string, data interface{}) (string, error) {
	klog.V(5).InfoS("Include", "r", r, "templateName", templateName, "data", data)
	return r.renderTemplate(templateName, data)
}

func (r RenderableObject) IncludeRenderableObject(obj RenderableObject) (output string) {
	klog.V(5).InfoS("called IncludeRenderableObject", "r", r, "obj", obj)
	renderString, _ := obj.renderString()
	return renderString
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

func labelSelector(s map[string]interface{}) string {
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(s, ls); err != nil {
		return fmt.Sprintf("%v", s)
	}
	return metav1.FormatLabelSelector(ls)
}

// tolerationMatchesTaint reports whether a single toleration covers a single taint, following
// https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#concepts
func tolerationMatchesTaint(toleration, taint map[string]interface{}) bool {
	if effect, _ := toleration["effect"].(string); effect != "" {
		taintEffect, _ := taint["effect"].(string)
		if effect != taintEffect {
			return false
		}
	}
	key, _ := toleration["key"].(string)
	operator, _ := toleration["operator"].(string)
	if operator == "" {
		operator = "Equal"
	}
	taintKey, _ := taint["key"].(string)
	switch operator {
	case "Exists":
		return key == "" || key == taintKey
	case "Equal":
		if key != taintKey {
			return false
		}
		value, _ := toleration["value"].(string)
		taintValue, _ := taint["value"].(string)
		return value == taintValue
	default:
		return false
	}
}

// taintsNotToleratedByPod returns the subset of nodeTaints that block scheduling or trigger
// eviction (NoSchedule/NoExecute) and aren't covered by any of the pod's tolerations.
// PreferNoSchedule is a soft preference, not a blocker, and is intentionally excluded.
func taintsNotToleratedByPod(nodeTaints, tolerations []interface{}) (result []interface{}) {
	for _, t := range nodeTaints {
		taint, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		effect, _ := taint["effect"].(string)
		if effect != "NoSchedule" && effect != "NoExecute" {
			continue
		}
		tolerated := false
		for _, tol := range tolerations {
			toleration, ok := tol.(map[string]interface{})
			if !ok {
				continue
			}
			if tolerationMatchesTaint(toleration, taint) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			result = append(result, taint)
		}
	}
	return result
}

// networkPolicySelectsPod reports whether a NetworkPolicy's spec.podSelector matches podLabels.
// podSelector is a full metav1.LabelSelector (matchLabels + matchExpressions), and an empty
// selector ({} -- no matchLabels, no matchExpressions) matches every Pod in the policy's
// namespace, per https://kubernetes.io/docs/concepts/services-networking/network-policies/ --
// metav1.LabelSelectorAsSelector already returns labels.Everything() for that case, so this uses
// real selector semantics rather than the isSubset helper (which is for a different direction of
// matching, see KubeGetServicesMatchingPod).
func networkPolicySelectsPod(policySpec map[string]interface{}, podLabels map[string]string) bool {
	selMap, _ := policySpec["podSelector"].(map[string]interface{})
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, ls); err != nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(podLabels))
}

// networkPolicyPolicyTypes normalizes NetworkPolicy spec.policyTypes, applying the documented
// default used when the field is omitted: Ingress always applies, and Egress applies only when
// the policy also defines an egress rule set. See
// https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#networkpolicyspec-v1-networking-k8s-io
func networkPolicyPolicyTypes(spec map[string]interface{}) []string {
	return policyTypesWithDefault(spec, "policyTypes")
}

// calicoPolicyTypes normalizes a Calico NetworkPolicy/GlobalNetworkPolicy's spec.types, applying
// the same defaulting rule documented for upstream NetworkPolicy (Ingress always applies, Egress
// only when the policy also defines egress rules) -- Calico's spec.types field mirrors
// spec.policyTypes here, just under a different name. See
// https://docs.tigera.io/calico/latest/reference/resources/networkpolicy.
func calicoPolicyTypes(spec map[string]interface{}) []string {
	return policyTypesWithDefault(spec, "types")
}

// policyTypesWithDefault is shared by networkPolicyPolicyTypes and calicoPolicyTypes -- both
// upstream NetworkPolicy and Calico's NetworkPolicy/GlobalNetworkPolicy apply the identical
// default (Ingress implied; Egress only if egress rules are present) under a differently-named
// spec field.
func policyTypesWithDefault(spec map[string]interface{}, typesKey string) []string {
	if rawTypes, ok := spec[typesKey].([]interface{}); ok && len(rawTypes) > 0 {
		types := make([]string, 0, len(rawTypes))
		for _, t := range rawTypes {
			if s, ok := t.(string); ok {
				types = append(types, s)
			}
		}
		return types
	}
	types := []string{"Ingress"}
	if _, hasEgress := spec["egress"]; hasEgress {
		types = append(types, "Egress")
	}
	return types
}

// calicoPolicySelectsPod reports whether a Calico NetworkPolicy/GlobalNetworkPolicy's spec.selector
// matches podLabels. Unlike Kubernetes LabelSelectors, Calico's selector is a small boolean
// expression language (see pkg/plugin/calicoselector), evaluated against Calico's own
// workload-endpoint label set -- which is the Pod's labels plus a synthetic
// "projectcalico.org/namespace" label -- not the Pod's bare labels. See
// https://docs.tigera.io/calico-cloud/network-policy/policy-tiers/tiered-policy. An empty
// selector matches everything, same as an absent podSelector for upstream NetworkPolicy.
// Unparseable selectors are conservatively treated as non-matching (logged at V(3)) rather than
// risking a false match.
func calicoPolicySelectsPod(spec map[string]interface{}, podLabels map[string]string, namespace string) bool {
	selectorStr, _ := spec["selector"].(string)
	sel, err := calicoselector.Parse(selectorStr)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to parse Calico selector", "selector", selectorStr)
		return false
	}
	return sel.Evaluate(withCalicoNamespaceLabel(podLabels, namespace))
}

// calicoNamespaceSelectorMatches reports whether a Calico GlobalNetworkPolicy's
// spec.namespaceSelector accepts a namespace, given that namespace's labels. An empty
// namespaceSelector matches every namespace (GlobalNetworkPolicy is cluster-scoped, so unlike the
// namespaced NetworkPolicy case there's no implicit namespace restriction to fall back on). See
// https://docs.tigera.io/calico-cloud/network-policy/policy-tiers/tiered-policy. Calico adds a
// synthetic "projectcalico.org/name" label to namespaces for use in such selectors.
func calicoNamespaceSelectorMatches(spec map[string]interface{}, namespace string, namespaceLabels map[string]string) bool {
	selectorStr, _ := spec["namespaceSelector"].(string)
	if selectorStr == "" {
		return true
	}
	sel, err := calicoselector.Parse(selectorStr)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to parse Calico namespaceSelector", "selector", selectorStr)
		return false
	}
	augmented := make(map[string]string, len(namespaceLabels)+1)
	for k, v := range namespaceLabels {
		augmented[k] = v
	}
	augmented["projectcalico.org/name"] = namespace
	return sel.Evaluate(augmented)
}

// crossplaneManagedResourceDrift compares a Crossplane managed resource's desired
// spec.forProvider configuration against its observed status.atProvider, see
// pkg/plugin/crossplanedrift for the comparison semantics. forProvider/atProvider are expected
// to be untyped map values as decoded off the unstructured object (nil/non-map is treated as
// absent).
func crossplaneManagedResourceDrift(forProvider, atProvider interface{}) crossplanedrift.Result {
	fp, _ := forProvider.(map[string]interface{})
	ap, _ := atProvider.(map[string]interface{})
	return crossplanedrift.Diff(fp, ap)
}

// crossplaneDriftLabel picks the operator-facing label/annotation for a non-empty drift result;
// see crossplanedrift.Label. syncedStatus is the managed resource's Synced condition ".status"
// value ("True"/"False"/"" if the condition is absent); managementPolicies is the untyped
// spec.managementPolicies list value (nil if absent, meaning full-control/default). Returns a
// dict with "Label" and "Annotation" keys for easy template consumption.
func crossplaneDriftLabel(syncedStatus string, managementPolicies interface{}) map[string]string {
	policies, _ := managementPolicies.([]interface{})
	label, annotation := crossplanedrift.Label(syncedStatus, policies)
	return map[string]string{"Label": label, "Annotation": annotation}
}

func withCalicoNamespaceLabel(podLabels map[string]string, namespace string) map[string]string {
	augmented := make(map[string]string, len(podLabels)+1)
	for k, v := range podLabels {
		augmented[k] = v
	}
	augmented["projectcalico.org/namespace"] = namespace
	return augmented
}

// ciliumRuleSpecs returns the individual Cilium Rule objects making up a CiliumNetworkPolicy or
// CiliumClusterwideNetworkPolicy -- its spec is either a single Rule (spec.endpointSelector,
// spec.ingress, ...) or, for multi-rule policies, a list of Rules under specs. See
// https://docs.cilium.io/en/stable/network/kubernetes/policy/.
func ciliumRuleSpecs(obj map[string]interface{}) (rules []map[string]interface{}) {
	if spec, ok := obj["spec"].(map[string]interface{}); ok {
		rules = append(rules, spec)
	}
	if specs, ok := obj["specs"].([]interface{}); ok {
		for _, s := range specs {
			if m, ok := s.(map[string]interface{}); ok {
				rules = append(rules, m)
			}
		}
	}
	return rules
}

// ciliumEndpointSelectorMatchesPod reports whether a Cilium Rule's endpointSelector matches
// podLabels. endpointSelector uses the same matchLabels/matchExpressions shape as a Kubernetes
// LabelSelector (https://docs.cilium.io/en/latest/security/policy/kubernetes/), and a
// missing/empty selector targets every endpoint, same as networkPolicySelectsPod's handling of an
// empty podSelector. Note: a policy authored against Cilium's own reserved label prefixes (e.g.
// "k8s:app") won't match here since podLabels are the Pod's bare labels -- acceptable for this
// compact signal, see the package doc.
func ciliumEndpointSelectorMatchesPod(rule map[string]interface{}, podLabels map[string]string) bool {
	selMap, _ := rule["endpointSelector"].(map[string]interface{})
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, ls); err != nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(podLabels))
}

// ciliumPolicySelectsPod reports whether any Rule in a CiliumNetworkPolicy/
// CiliumClusterwideNetworkPolicy object selects podLabels, and the union of restricted
// directions across the matching Rule(s). Unlike upstream NetworkPolicy, Cilium has no implied
// default direction: a Rule only restricts ingress when it carries an ingress or ingressDeny rule
// list, and only restricts egress when it carries egress or egressDeny -- a bare endpointSelector
// with no rule lists selects the endpoint but enforces nothing.
func ciliumPolicySelectsPod(obj map[string]interface{}, podLabels map[string]string) (matches bool, directions []string) {
	ingress, egress := false, false
	for _, rule := range ciliumRuleSpecs(obj) {
		if !ciliumEndpointSelectorMatchesPod(rule, podLabels) {
			continue
		}
		matches = true
		if _, ok := rule["ingress"]; ok {
			ingress = true
		}
		if _, ok := rule["ingressDeny"]; ok {
			ingress = true
		}
		if _, ok := rule["egress"]; ok {
			egress = true
		}
		if _, ok := rule["egressDeny"]; ok {
			egress = true
		}
	}
	if ingress {
		directions = append(directions, "ingress")
	}
	if egress {
		directions = append(directions, "egress")
	}
	return matches, directions
}

// ciliumPolicyDirectionsForTemplate is the template-callable wrapper for ciliumPolicySelectsPod,
// used to render the restricted directions for a CiliumNetworkPolicy/CiliumClusterwideNetworkPolicy
// already known to select the Pod (see KubeGetCiliumNetworkPoliciesMatchingPod).
func ciliumPolicyDirectionsForTemplate(obj map[string]interface{}, podLabels map[string]interface{}) []string {
	_, directions := ciliumPolicySelectsPod(obj, stringifyLabels(podLabels))
	return directions
}

func stringifyLabels(labels map[string]interface{}) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

// parseTLSSecretCertificate inspects a Secret expected to be type kubernetes.io/tls and
// returns both full certificate detail (for Secret.tmpl's own display) and consistency
// flags against an optional expected hostname (for Ingress/Gateway callers). hostname == ""
// skips the hostname-match check and is used by Secret.tmpl, which has no "expected host" of
// its own.
func (cfg *RenderConfig) parseTLSSecretCertificate(secret RenderableObject, hostname string) map[string]interface{} {
	result := map[string]interface{}{
		"Exists":          false,
		"WrongType":       false,
		"ActualType":      "",
		"MissingKeys":     []string{},
		"ParseError":      "",
		"Subject":         "",
		"Issuer":          "",
		"SerialNumber":    "",
		"NotBefore":       time.Time{},
		"NotAfter":        time.Time{},
		"DNSNames":        []string{},
		"AltDNSNames":     []string{},
		"IPAddresses":     []string{},
		"KeyAlgorithm":    "",
		"SelfSigned":      false,
		"Expired":         false,
		"MatchesHostname": false,
	}
	if secret.Object == nil {
		return result
	}
	result["Exists"] = true

	actualType, _ := secret.Object["type"].(string)
	result["ActualType"] = actualType
	if actualType != "kubernetes.io/tls" {
		result["WrongType"] = true
		return result
	}

	data, _ := secret.Object["data"].(map[string]interface{})
	var missingKeys []string
	for _, key := range []string{"tls.crt", "tls.key"} {
		if _, ok := data[key]; !ok {
			missingKeys = append(missingKeys, key)
		}
	}
	if missingKeys != nil {
		result["MissingKeys"] = missingKeys
		return result
	}

	crtEncoded, ok := data["tls.crt"].(string)
	if !ok {
		result["ParseError"] = "tls.crt is not a string"
		return result
	}
	crtDecoded, err := base64.StdEncoding.DecodeString(crtEncoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode tls.crt: %v", err)
		return result
	}
	block, _ := pem.Decode(crtDecoded)
	if block == nil {
		result["ParseError"] = "failed to PEM-decode tls.crt"
		return result
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse leaf certificate: %v", err)
		return result
	}

	var ipAddresses []string
	for _, ip := range cert.IPAddresses {
		ipAddresses = append(ipAddresses, ip.String())
	}

	dnsNames := cert.DNSNames
	if dnsNames == nil {
		dnsNames = []string{}
	}
	if ipAddresses == nil {
		ipAddresses = []string{}
	}
	var altDNSNames []string
	for _, dns := range dnsNames {
		if dns != cert.Subject.CommonName {
			altDNSNames = append(altDNSNames, dns)
		}
	}
	if altDNSNames == nil {
		altDNSNames = []string{}
	}

	result["Subject"] = cert.Subject.String()
	result["Issuer"] = cert.Issuer.String()
	result["SerialNumber"] = cert.SerialNumber.String()
	result["NotBefore"] = cert.NotBefore
	result["NotAfter"] = cert.NotAfter
	result["DNSNames"] = dnsNames
	result["AltDNSNames"] = altDNSNames
	result["IPAddresses"] = ipAddresses
	result["KeyAlgorithm"] = cert.PublicKeyAlgorithm.String()
	result["SelfSigned"] = bytes.Equal(cert.RawIssuer, cert.RawSubject)
	result["Expired"] = cert.NotAfter.Before(cfg.Now())

	if hostname == "" {
		result["MatchesHostname"] = true
	} else {
		result["MatchesHostname"] = cert.VerifyHostname(hostname) == nil
	}

	return result
}

// parseDockerConfigSecret inspects a Secret expected to be type kubernetes.io/dockerconfigjson
// or the legacy kubernetes.io/dockercfg and extracts the configured registry hostnames only --
// never credentials, since those would end up in copy-pasted output. A missing/wrong registry
// entry is a common cause of ImagePullBackOff; Pod.tmpl's imagePullSecrets check only validates
// the Secret's type, not its contents.
func parseDockerConfigSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"Exists":     false,
		"WrongType":  false,
		"ActualType": "",
		"MissingKey": "",
		"ParseError": "",
		"Registries": []string{},
	}
	if secret.Object == nil {
		return result
	}
	result["Exists"] = true

	actualType, _ := secret.Object["type"].(string)
	result["ActualType"] = actualType

	var dataKey string
	switch actualType {
	case "kubernetes.io/dockerconfigjson":
		dataKey = ".dockerconfigjson"
	case "kubernetes.io/dockercfg":
		dataKey = ".dockercfg"
	default:
		result["WrongType"] = true
		return result
	}

	data, _ := secret.Object["data"].(map[string]interface{})
	encoded, ok := data[dataKey].(string)
	if !ok {
		result["MissingKey"] = dataKey
		return result
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode %s: %v", dataKey, err)
		return result
	}

	var auths map[string]interface{}
	if actualType == "kubernetes.io/dockerconfigjson" {
		var wrapper struct {
			Auths map[string]interface{} `json:"auths"`
		}
		if err := json.Unmarshal(decoded, &wrapper); err != nil {
			result["ParseError"] = fmt.Sprintf("failed to parse %s: %v", dataKey, err)
			return result
		}
		auths = wrapper.Auths
	} else if err := json.Unmarshal(decoded, &auths); err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse %s: %v", dataKey, err)
		return result
	}

	var registries []string
	for registry := range auths {
		registries = append(registries, registry)
	}
	sort.Strings(registries)
	if registries == nil {
		registries = []string{}
	}
	result["Registries"] = registries
	return result
}

// decodeSecretDataField base64-decodes data[key] as a Secret's data entry is encoded on the
// wire, returning ok=false if the key is absent or isn't a string. A key present but undecodable
// base64 is reported as present with an empty decoded value, since callers here only need to
// tell "absent" from "empty" from "has content", not surface a parse error for it.
func decodeSecretDataField(data map[string]interface{}, key string) (value string, ok bool) {
	encoded, ok := data[key].(string)
	if !ok {
		return "", false
	}
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	return string(decoded), true
}

// parseBasicAuthSecret inspects a Secret expected to be type kubernetes.io/basic-auth and
// reports only whether its username/password entries are present and non-empty -- never their
// values. An empty value (e.g. `--from-literal=password=`) is distinguished from a missing key:
// both are broken, but they point at different mistakes.
func parseBasicAuthSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"HasUsername":   false,
		"UsernameEmpty": false,
		"HasPassword":   false,
		"PasswordEmpty": false,
	}
	if secret.Object == nil {
		return result
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	if v, ok := decodeSecretDataField(data, "username"); ok {
		result["HasUsername"] = true
		result["UsernameEmpty"] = v == ""
	}
	if v, ok := decodeSecretDataField(data, "password"); ok {
		result["HasPassword"] = true
		result["PasswordEmpty"] = v == ""
	}
	return result
}

// parseSSHAuthSecret inspects a Secret expected to be type kubernetes.io/ssh-auth and reports
// whether its ssh-privatekey entry is present and parses as an SSH private key -- never the key
// material itself. KeyType/Fingerprint are derived from the *public* half of the key pair (the
// SHA256 fingerprint is the same one `ssh-keygen -lf` prints), mirroring the level of detail
// parseTLSSecretCertificate reports for kubernetes.io/tls (algorithm plus an identifying value),
// just without the certificate-specific fields (subject/issuer/validity) an SSH key doesn't have.
func parseSSHAuthSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"Exists":      false,
		"ParseError":  "",
		"KeyType":     "",
		"Fingerprint": "",
	}
	if secret.Object == nil {
		return result
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	encoded, ok := data["ssh-privatekey"].(string)
	if !ok {
		return result
	}
	result["Exists"] = true
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode ssh-privatekey: %v", err)
		return result
	}
	signer, err := ssh.ParsePrivateKey(decoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse ssh-privatekey: %v", err)
		return result
	}
	result["KeyType"] = signer.PublicKey().Type()
	result["Fingerprint"] = ssh.FingerprintSHA256(signer.PublicKey())
	return result
}

// parseServiceAccountTokenSecret inspects a Secret expected to be type
// kubernetes.io/service-account-token. Its data.token entry is populated asynchronously by the
// legacy TokenController once the kubernetes.io/service-account.name annotation names an
// existing ServiceAccount, so a freshly created Secret can legitimately carry the annotation but
// no token yet -- the template reports that as "waiting on controller", not as an error.
func parseServiceAccountTokenSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"HasServiceAccountName": false,
		"ServiceAccountName":    "",
		"HasToken":              false,
	}
	if secret.Object == nil {
		return result
	}
	if name, ok := secret.Annotations()["kubernetes.io/service-account.name"].(string); ok && name != "" {
		result["HasServiceAccountName"] = true
		result["ServiceAccountName"] = name
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	if token, ok := data["token"].(string); ok && token != "" {
		result["HasToken"] = true
	}
	return result
}

var (
	bootstrapTokenNameRegexp   = regexp.MustCompile(`^bootstrap-token-([a-z0-9]{6})$`)
	bootstrapTokenIDRegexp     = regexp.MustCompile(`^[a-z0-9]{6}$`)
	bootstrapTokenSecretRegexp = regexp.MustCompile(`^[a-z0-9]{16}$`)
)

// parseBootstrapTokenSecret inspects a Secret expected to be type bootstrap.kubernetes.io/token
// against the structural rules the bootstrap-token authenticator enforces at read time (see
// https://kubernetes.io/docs/reference/access-authn-authz/bootstrap-tokens/): it must live in
// kube-system, be named bootstrap-token-<token-id>, and carry a token-id/token-secret pair
// matching that id -- a Secret violating any of these is silently never honoured as a valid
// token, with no Kubernetes-side error to surface. token-secret's value is never reported, only
// whether it's present and well-formed. Expired is computed against cfg.Now() rather than
// time.Now() so it stays pinned under --test-hack.
func (cfg *RenderConfig) parseBootstrapTokenSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"NamespaceOK":         false,
		"ActualNamespace":     "",
		"NameOK":              false,
		"MissingKeys":         []string{},
		"TokenID":             "",
		"TokenIDValid":        false,
		"TokenIDMatchesName":  false,
		"TokenSecretPresent":  false,
		"TokenSecretValid":    false,
		"HasExpiration":       false,
		"Expiration":          time.Time{},
		"Expired":             false,
		"UsageAuthentication": false,
		"UsageSigning":        false,
	}
	if secret.Object == nil {
		return result
	}

	namespace := secret.Namespace()
	result["ActualNamespace"] = namespace
	result["NamespaceOK"] = namespace == "kube-system"

	nameMatch := bootstrapTokenNameRegexp.FindStringSubmatch(secret.Name())
	result["NameOK"] = nameMatch != nil

	data, _ := secret.Object["data"].(map[string]interface{})
	decode := func(key string) (string, bool) {
		encoded, ok := data[key].(string)
		if !ok {
			return "", false
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
		return string(decoded), true
	}

	var missingKeys []string
	tokenID, ok := decode("token-id")
	if !ok {
		missingKeys = append(missingKeys, "token-id")
	} else {
		result["TokenID"] = tokenID
		result["TokenIDValid"] = bootstrapTokenIDRegexp.MatchString(tokenID)
		if nameMatch != nil {
			result["TokenIDMatchesName"] = tokenID == nameMatch[1]
		}
	}
	tokenSecret, ok := decode("token-secret")
	if !ok {
		missingKeys = append(missingKeys, "token-secret")
	} else {
		result["TokenSecretPresent"] = true
		result["TokenSecretValid"] = bootstrapTokenSecretRegexp.MatchString(tokenSecret)
	}
	if missingKeys != nil {
		result["MissingKeys"] = missingKeys
	}

	if expiration, ok := decode("expiration"); ok && expiration != "" {
		if t, err := time.Parse(time.RFC3339, expiration); err == nil {
			result["HasExpiration"] = true
			result["Expiration"] = t
			result["Expired"] = t.Before(cfg.Now())
		}
	}

	if v, ok := decode("usage-bootstrap-authentication"); ok {
		result["UsageAuthentication"] = v == "true"
	}
	if v, ok := decode("usage-bootstrap-signing"); ok {
		result["UsageSigning"] = v == "true"
	}

	return result
}

// secretDataKeys returns the sorted union of a Secret's data and stringData key names, for
// Opaque secrets where there's no dedicated type-specific section to render -- listing the keys
// present gives a compact hint at the Secret's shape without exposing any values.
func secretDataKeys(secret RenderableObject) []string {
	var keys []string
	if secret.Object == nil {
		return keys
	}
	seen := map[string]bool{}
	for _, field := range []string{"data", "stringData"} {
		m, _ := secret.Object[field].(map[string]interface{})
		for key := range m {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// newCertificateEntry returns the zero-value result map for a single ".crt" entry, keyed the
// same way regardless of which resource (Secret or ConfigMap) it was scanned from.
func newCertificateEntry(name string) map[string]interface{} {
	return map[string]interface{}{
		"Name":         name,
		"ParseError":   "",
		"Subject":      "",
		"Issuer":       "",
		"SerialNumber": "",
		"NotBefore":    time.Time{},
		"NotAfter":     time.Time{},
		"AltDNSNames":  []string{},
		"IPAddresses":  []string{},
		"KeyAlgorithm": "",
		"SelfSigned":   false,
		"Expired":      false,
	}
}

// parseCertificateBytesInto PEM-decodes and parses decoded as an X.509 certificate, filling
// entry's fields in place, or setting entry["ParseError"] on failure. name is only used to
// identify the source key in error messages. Expired is computed against cfg.Now() rather than
// time.Now() so it stays pinned under ApplyTestHack.
func (cfg *RenderConfig) parseCertificateBytesInto(entry map[string]interface{}, name string, decoded []byte) {
	block, _ := pem.Decode(decoded)
	if block == nil {
		entry["ParseError"] = fmt.Sprintf("failed to PEM-decode %s", name)
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		entry["ParseError"] = fmt.Sprintf("failed to parse certificate in %s: %v", name, err)
		return
	}

	var ipAddresses []string
	for _, ip := range cert.IPAddresses {
		ipAddresses = append(ipAddresses, ip.String())
	}
	var altDNSNames []string
	for _, dns := range cert.DNSNames {
		if dns != cert.Subject.CommonName {
			altDNSNames = append(altDNSNames, dns)
		}
	}

	entry["Subject"] = cert.Subject.String()
	entry["Issuer"] = cert.Issuer.String()
	entry["SerialNumber"] = cert.SerialNumber.String()
	entry["NotBefore"] = cert.NotBefore
	entry["NotAfter"] = cert.NotAfter
	if len(altDNSNames) > 0 {
		entry["AltDNSNames"] = altDNSNames
	}
	if len(ipAddresses) > 0 {
		entry["IPAddresses"] = ipAddresses
	}
	entry["KeyAlgorithm"] = cert.PublicKeyAlgorithm.String()
	entry["SelfSigned"] = bytes.Equal(cert.RawIssuer, cert.RawSubject)
	entry["Expired"] = cert.NotAfter.Before(cfg.Now())
}

// certificatesInSecret scans a Secret's data for keys ending in ".crt", regardless of the
// Secret's declared type, and parses each as an X.509 certificate. This covers secrets that
// don't use the standard kubernetes.io/tls layout, e.g. cert-manager's internal CA secrets,
// which are type Opaque and hold a ca.crt alongside a tls.crt/tls.key pair.
func (cfg *RenderConfig) certificatesInSecret(secret RenderableObject) []map[string]interface{} {
	var results []map[string]interface{}
	if secret.Object == nil {
		return results
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	if data == nil {
		return results
	}

	var keys []string
	for key := range data {
		if strings.HasSuffix(key, ".crt") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		entry := newCertificateEntry(key)
		results = append(results, entry)

		encoded, ok := data[key].(string)
		if !ok {
			entry["ParseError"] = fmt.Sprintf("%s is not a string", key)
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			entry["ParseError"] = fmt.Sprintf("failed to base64-decode %s: %v", key, err)
			continue
		}
		cfg.parseCertificateBytesInto(entry, key, decoded)
	}

	return results
}

// certificatesInConfigMap scans a ConfigMap's data and binaryData for keys ending in ".crt"
// and parses each as an X.509 certificate. Unlike Secret, ConfigMap.data values are plain
// text (not base64) while ConfigMap.binaryData values are base64, matching the Kubernetes API
// convention for the two fields.
func (cfg *RenderConfig) certificatesInConfigMap(configMap RenderableObject) []map[string]interface{} {
	var results []map[string]interface{}
	if configMap.Object == nil {
		return results
	}

	type source struct {
		key     string
		decoded []byte
		err     error
	}
	var sources []source

	if data, ok := configMap.Object["data"].(map[string]interface{}); ok {
		for key, v := range data {
			if !strings.HasSuffix(key, ".crt") {
				continue
			}
			s, ok := v.(string)
			if !ok {
				sources = append(sources, source{key: key, err: fmt.Errorf("%s is not a string", key)})
				continue
			}
			sources = append(sources, source{key: key, decoded: []byte(s)})
		}
	}
	if binaryData, ok := configMap.Object["binaryData"].(map[string]interface{}); ok {
		for key, v := range binaryData {
			if !strings.HasSuffix(key, ".crt") {
				continue
			}
			s, ok := v.(string)
			if !ok {
				sources = append(sources, source{key: key, err: fmt.Errorf("%s is not a string", key)})
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				sources = append(sources, source{key: key, err: fmt.Errorf("failed to base64-decode %s: %v", key, err)})
				continue
			}
			sources = append(sources, source{key: key, decoded: decoded})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].key < sources[j].key })

	for _, s := range sources {
		entry := newCertificateEntry(s.key)
		results = append(results, entry)
		if s.err != nil {
			entry["ParseError"] = s.err.Error()
			continue
		}
		cfg.parseCertificateBytesInto(entry, s.key, s.decoded)
	}

	return results
}

// certificateInCSR parses a CertificateSigningRequest's status.certificate (base64-encoded PEM,
// populated once a signer issues the certificate) as an X.509 certificate. Returns nil if the
// CSR hasn't been issued yet.
func (cfg *RenderConfig) certificateInCSR(csr RenderableObject) map[string]interface{} {
	certEncoded, ok := csr.Status()["certificate"].(string)
	if !ok || certEncoded == "" {
		return nil
	}

	entry := newCertificateEntry("certificate")
	decoded, err := base64.StdEncoding.DecodeString(certEncoded)
	if err != nil {
		entry["ParseError"] = fmt.Sprintf("failed to base64-decode certificate: %v", err)
		return entry
	}
	cfg.parseCertificateBytesInto(entry, "certificate", decoded)
	return entry
}

// certificateRequestInCSR parses a CertificateSigningRequest's spec.request (a base64-encoded
// PKCS#10 CSR) to surface what's actually being requested -- the subject, SANs, and key
// algorithm -- which is available for Pending and Denied requests too, unlike
// certificateInCSR's status.certificate.
func certificateRequestInCSR(csr RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"ParseError":   "",
		"Subject":      "",
		"AltDNSNames":  []string{},
		"IPAddresses":  []string{},
		"KeyAlgorithm": "",
	}

	encoded, ok := csr.Spec()["request"].(string)
	if !ok || encoded == "" {
		result["ParseError"] = "spec.request is empty"
		return result
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode request: %v", err)
		return result
	}
	block, _ := pem.Decode(decoded)
	if block == nil {
		result["ParseError"] = "failed to PEM-decode request"
		return result
	}
	csrRequest, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse certificate request: %v", err)
		return result
	}

	var ipAddresses []string
	for _, ip := range csrRequest.IPAddresses {
		ipAddresses = append(ipAddresses, ip.String())
	}
	var altDNSNames []string
	for _, dns := range csrRequest.DNSNames {
		if dns != csrRequest.Subject.CommonName {
			altDNSNames = append(altDNSNames, dns)
		}
	}
	if altDNSNames == nil {
		altDNSNames = []string{}
	}
	if ipAddresses == nil {
		ipAddresses = []string{}
	}

	result["Subject"] = csrRequest.Subject.String()
	result["AltDNSNames"] = altDNSNames
	result["IPAddresses"] = ipAddresses
	result["KeyAlgorithm"] = csrRequest.PublicKeyAlgorithm.String()
	return result
}
