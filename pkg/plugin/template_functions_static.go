package plugin

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/go-sprout/sprout"
	"github.com/robfig/cron/v3"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
)

var durationRound = DefaultDurationRound()

var nowFunc = time.Now

// SetNowFunc is a helper method for tests
func SetNowFunc(f func() time.Time) (revertFunc func()) {
	nowFunc = f
	return func() {
		nowFunc = time.Now
	}
}

func DefaultDurationRound() func(duration interface{}) string {
	return (sprout.GenericFuncMap()["durationRound"]).(func(duration interface{}) string)
}

// SetDurationRound is a helper method for tests
func SetDurationRound(f func(duration interface{}) string) (revertFunc func()) {
	durationRound = f
	return func() {
		durationRound = DefaultDurationRound()
	}
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"green":                     color.GreenString,
		"yellow":                    color.YellowString,
		"red":                       color.RedString,
		"cyan":                      color.CyanString,
		"blue":                      color.BlueString,
		"bold":                      color.New(color.Bold).SprintfFunc(),
		"colorAgo":                  colorAgo,
		"colorDuration":             colorDuration,
		"startedAfterClause":        startedAfterClause,
		"colorBool":                 colorBool,
		"colorKeyword":              colorKeyword,
		"markRed":                   markRed,
		"markYellow":                markYellow,
		"markGreen":                 markGreen,
		"redIf":                     redIf,
		"redBoldIf":                 redBoldIf,
		"signalName":                signalName,
		"isStatusConditionHealthy":  isStatusConditionHealthy,
		"quantityToFloat64":         quantityToFloat64,
		"quantityToInt64":           quantityToInt64,
		"percent":                   percent,
		"colorPercent":              colorPercent,
		"humanizeSI":                humanizeSI,
		"getMatchingItemInMapList":  getMatchingItemInMapList,
		"sortMapListByKeysValue":    sortMapListByKeysValue,
		"addFloat64":                addFloat64,
		"subFloat64":                subFloat64,
		"divFloat64":                divFloat64,
		"ip":                        ip,
		"agoSuffix":                 agoSuffix,
		"forOrSince":                forOrSince,
		"relativeTime":              relativeTime,
		"labelSelector":             labelSelector,
		"taintsNotToleratedByPod":   taintsNotToleratedByPod,
		"networkPolicyPolicyTypes":  networkPolicyPolicyTypes,
		"cronNextTime":              cronNextTime,
		"withinLastHour":            withinLastHour,
		"parseTLSSecretCertificate": parseTLSSecretCertificate,
		"certificatesInSecret":      certificatesInSecret,
		"certificatesInConfigMap":   certificatesInConfigMap,
		"certificateInCSR":          certificateInCSR,
		"certificateRequestInCSR":   certificateRequestInCSR,
	}
}

func ip(ip string) string {
	if viper.GetBool("test-hack") {
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
		condition["type"] == "Failed",                                   // Failed Jobs has this condition

		// Conditions from "Node Problem Detector"
		condition["type"] == "CorruptDockerImage",
		condition["type"] == "CorruptDockerOverlay2",
		condition["type"] == "DockerContainerStartupFailure",
		condition["type"] == "DockerHung",
		condition["type"] == "Ext4Error",
		condition["type"] == "Ext4Warning",
		condition["type"] == "FilesystemIsReadOnly",
		condition["type"] == "IOError",
		condition["type"] == "NvmeTcpProblem",
		condition["type"] == "EdgeDecryptionProblem",
		condition["type"] == "IOErrorProblem",
		condition["type"] == "NetworkProblem",
		condition["type"] == "KernelDeadlock",
		condition["type"] == "KernelOops",
		condition["type"] == "MemoryReadError",
		condition["type"] == "OOMKilling",
		condition["type"] == "ReadonlyFilesystem",
		condition["type"] == "TaskHung",
		condition["type"] == "UnregisterNetDevice",
		condition["type"] == "FrequentDockerRestart",
		condition["type"] == "FilesystemCorruptionProblem",
		condition["type"] == "FrequentContainerdRestart",
		condition["type"] == "FrequentKubeletRestart",
		condition["type"] == "RebootScheduled",
		condition["type"] == "KubeletProblem",
		condition["type"] == "TerminateScheduled",
		condition["type"] == "ContainerRuntimeProblem",
		condition["type"] == "RedeployScheduled",
		condition["type"] == "PreemptScheduled",
		condition["type"] == "FreezeScheduled",
		condition["type"] == "FrequentUnregisterNetDevice":
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

func colorAgo(kubeDate string) string {
	t, _ := time.ParseInLocation("2006-01-02T15:04:05Z", kubeDate, time.UTC)
	if viper.GetBool("absolute-time") {
		return t.Format("2006-01-02T15:04:05Z")
	}
	duration := time.Since(t).Round(time.Second)
	return colorDuration(duration)
}

var startedAfterClauseFunc = defaultStartedAfterClause

// defaultStartedAfterClause renders the ", started after <duration>" suffix of the status
// summary line. Both timestamps come off the wire at 1-second resolution, so on a live cluster
// whether this clause appears at all hinges on whether the pod's creation and kubelet-acknowledge
// timestamps land in the same wall-clock second -- a coin flip e2e tests can't control. Tests
// replace this func (see SetStartedAfterClause) so the clause is deterministic instead of tied to
// that real scheduling latency.
func defaultStartedAfterClause(createdKubeDate, startedKubeDate string) string {
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
	return ", started after " + colorDuration(duration)
}

// SetStartedAfterClause is a helper method for tests
func SetStartedAfterClause(f func(createdKubeDate, startedKubeDate string) string) (revertFunc func()) {
	startedAfterClauseFunc = f
	return func() {
		startedAfterClauseFunc = defaultStartedAfterClause
	}
}

func startedAfterClause(createdKubeDate, startedKubeDate string) string {
	return startedAfterClauseFunc(createdKubeDate, startedKubeDate)
}

func ago(t time.Time) string {
	duration := time.Since(t).Round(time.Second)
	return durationRound(duration.String())
}

func colorDuration(duration time.Duration) string {
	str := durationRound(duration.String())
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

func agoSuffix() string {
	if viper.GetBool("absolute-time") {
		return ""
	}
	return " ago"
}

func forOrSince() string {
	if viper.GetBool("absolute-time") {
		return "since"
	}
	return "for"
}

func withinLastHour(kubeDate interface{}) bool {
	s, ok := kubeDate.(string)
	if !ok || s == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return false
	}
	d := nowFunc().Sub(t)
	return d >= 0 && d < time.Hour
}

func relativeTime(kubeDate string) string {
	if viper.GetBool("absolute-time") {
		return ""
	}
	t, _ := time.ParseInLocation("2006-01-02T15:04:05Z", kubeDate, time.UTC)
	duration := time.Since(t).Round(time.Second)
	return fmt.Sprintf(" (%s ago)", colorDuration(duration))
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

func cronNextTime(schedule string, timezone interface{}) string {
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
	now := nowFunc()
	next := sched.Next(now)
	if next.IsZero() {
		return ""
	}
	nextStr := next.UTC().Format("2006-01-02T15:04:05Z")
	if viper.GetBool("absolute-time") {
		return nextStr
	}
	duration := next.Sub(now).Round(time.Second)
	return fmt.Sprintf("%s (in %s)", nextStr, colorDuration(duration))
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
	if rawTypes, ok := spec["policyTypes"].([]interface{}); ok && len(rawTypes) > 0 {
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

// parseTLSSecretCertificate inspects a Secret expected to be type kubernetes.io/tls and
// returns both full certificate detail (for Secret.tmpl's own display) and consistency
// flags against an optional expected hostname (for Ingress/Gateway callers). hostname == ""
// skips the hostname-match check and is used by Secret.tmpl, which has no "expected host" of
// its own.
func parseTLSSecretCertificate(secret RenderableObject, hostname string) map[string]interface{} {
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

	if hostname == "" {
		result["MatchesHostname"] = true
	} else {
		result["MatchesHostname"] = cert.VerifyHostname(hostname) == nil
	}

	return result
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
	}
}

// parseCertificateBytesInto PEM-decodes and parses decoded as an X.509 certificate, filling
// entry's fields in place, or setting entry["ParseError"] on failure. name is only used to
// identify the source key in error messages.
func parseCertificateBytesInto(entry map[string]interface{}, name string, decoded []byte) {
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
}

// certificatesInSecret scans a Secret's data for keys ending in ".crt", regardless of the
// Secret's declared type, and parses each as an X.509 certificate. This covers secrets that
// don't use the standard kubernetes.io/tls layout, e.g. cert-manager's internal CA secrets,
// which are type Opaque and hold a ca.crt alongside a tls.crt/tls.key pair.
func certificatesInSecret(secret RenderableObject) []map[string]interface{} {
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
		parseCertificateBytesInto(entry, key, decoded)
	}

	return results
}

// certificatesInConfigMap scans a ConfigMap's data and binaryData for keys ending in ".crt"
// and parses each as an X.509 certificate. Unlike Secret, ConfigMap.data values are plain
// text (not base64) while ConfigMap.binaryData values are base64, matching the Kubernetes API
// convention for the two fields.
func certificatesInConfigMap(configMap RenderableObject) []map[string]interface{} {
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
		parseCertificateBytesInto(entry, s.key, s.decoded)
	}

	return results
}

// certificateInCSR parses a CertificateSigningRequest's status.certificate (base64-encoded PEM,
// populated once a signer issues the certificate) as an X.509 certificate. Returns nil if the
// CSR hasn't been issued yet.
func certificateInCSR(csr RenderableObject) map[string]interface{} {
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
	parseCertificateBytesInto(entry, "certificate", decoded)
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
