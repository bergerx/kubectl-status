package plugin

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cast"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	resourcehelper "k8s.io/component-helpers/resource"
	"k8s.io/klog/v2"
)

// evictionSignal correlates one kubelet eviction-hard threshold against the node's current
// headroom for that resource -- e.g. the "nodefs.available<10%" config value against the node's
// actual free disk right now. The two numbers otherwise live in separate kubelet API responses
// (configz vs stats/summary), so this lets a template render them as one line instead of forcing
// the reader to cross-reference two blocks. OK is false when there wasn't enough data to correlate
// (an unparseable threshold, or a percent-form threshold with no known total to normalize against);
// callers should fall back to showing Threshold alone in that case.
type evictionSignal struct {
	Threshold string
	Current   string
	AtRisk    bool // current headroom is within 1.5x of tripping the threshold
	Tripped   bool // current headroom is already at or past the threshold
	OK        bool
}

// evictionHeadroom builds an evictionSignal for one eviction-hard threshold. threshold is the raw
// configured value as kubelet reports it, either a percentage ("10%") or a resource.Quantity
// ("100Mi"). current is how much of that resource is free right now -- pass a negative value when
// this isn't actually known (e.g. stats/summary wasn't fetched), so a missing measurement isn't
// mistaken for a real zero and reported as an imminent eviction. total is current's capacity, only
// used to normalize a percentage threshold ("10%" of what?); pass 0 for total when unknown or when
// threshold isn't a percentage. unit is "B" for byte quantities or "" for bare counts.
func evictionHeadroom(threshold string, current, total float64, unit string) evictionSignal {
	sig := evictionSignal{Threshold: threshold}
	if current < 0 {
		return sig
	}
	isPercent := strings.HasSuffix(threshold, "%")
	var thresholdValue float64
	if isPercent {
		v, err := strconv.ParseFloat(strings.TrimSuffix(threshold, "%"), 64)
		if err != nil {
			return sig
		}
		thresholdValue = v
	} else {
		quantity, err := resource2.ParseQuantity(threshold)
		if err != nil {
			return sig
		}
		thresholdValue = float64(quantity.MilliValue()) / 1000
	}
	currentValue := current
	if isPercent {
		if total <= 0 {
			return sig
		}
		currentValue = percent(current, total)
		sig.Current = fmt.Sprintf("%.0f%% free", currentValue)
	} else if unit == "B" {
		sig.Current = humanizeSI("B", current) + " free"
	} else {
		sig.Current = humanizeSI("", current) + " free"
	}
	sig.Tripped = currentValue <= thresholdValue
	sig.AtRisk = currentValue <= thresholdValue*1.5
	sig.OK = true
	return sig
}

// evictionAnnotation renders evictionHeadroom's verdict for one eviction-hard signal as a
// ready-to-print, already-colored suffix (e.g. " (10% TRIPPED: 5% free)"), or "" when the signal
// isn't at risk or there's nothing to correlate against -- a healthy node gets no annotation at
// all. Colored here with color.New(...).Sprint rather than the template-level "bold" func: the
// message always contains a literal "%" (from the threshold/current percentages), and "bold"
// unconditionally calls fmt.Sprintf even with no extra args, which misparses "% " followed by
// certain letters (T, f, s, ...) as a format verb with a missing argument and corrupts the output
// (e.g. "10% TRIPPED" -> "10%!T(MISSING)RIPPED"). Sprint never parses verbs, so it's safe here
// regardless of what the message happens to contain.
func evictionAnnotation(threshold string, current, total float64, unit string) string {
	sig := evictionHeadroom(threshold, current, total, unit)
	switch {
	case sig.Tripped:
		return color.New(color.FgRed, color.Bold).Sprint(fmt.Sprintf(" (%s TRIPPED: %s)", sig.Threshold, sig.Current))
	case sig.AtRisk:
		return color.New(color.FgYellow).Sprint(fmt.Sprintf(" (nearing %s: %s)", sig.Threshold, sig.Current))
	default:
		return ""
	}
}

// quotaHeadroomReport is what quotaRolloutHeadroom found for one workload: how many Pods the
// workload still has to create before its rollout can finish, and the ResourceQuotas that don't
// currently have room for them. Quotas is empty whenever every quota in the namespace has room
// (the common case), so templates can render nothing at all without inspecting further.
type quotaHeadroomReport struct {
	ExtraPods int
	Quotas    []quotaHeadroomQuota
}

// quotaHeadroomQuota is one ResourceQuota that is short on at least one of the resources the
// pending Pods would consume. Only the short resources are listed -- a quota that tracks ten
// resources and is short on one shouldn't print the nine that fit.
type quotaHeadroomQuota struct {
	Name       string
	Shortfalls []quotaShortfall
}

// quotaShortfall is one resource of one quota. The figures are resource.Quantity strings, i.e. the
// same canonical form the quota itself is written in ("512Mi", "500m"), so the reader can compare
// them against their own manifests and against kubectl's own quota output.
type quotaShortfall struct {
	Resource string
	Need     string
	Free     string
	Used     string
	Hard     string
}

// quotaRolloutHeadroom answers "will the Pods this rollout still has to create fit in the
// namespace's ResourceQuota?" -- the warning a reader wants *before* the rollout wedges on
// FailedCreate. quotas are the namespace's ResourceQuota objects and workload is a Deployment or
// StatefulSet, all as unstructured maps, which are converted to their API types so that the
// per-Pod resource total comes from the same k8s.io/component-helpers/resource implementation the
// scheduler and quota controller use (sidecars, init containers, pod-level resources and
// RuntimeClass overhead all have non-obvious rules there that aren't worth restating).
//
// The comparison is deliberately conservative, because a false "you're out of quota" on a workload
// that is merely slow to roll out is worse than saying nothing:
//
//   - Quotas carrying spec.scopes or spec.scopeSelector are skipped entirely. Whether these Pods
//     fall in a scope depends on fields (priorityClassName, BestEffort-ness, termination) whose
//     matching rules live in k8s.io/kubernetes and can't be imported, and guessing wrong means
//     blaming a quota that never applied to these Pods.
//   - A quota resource the Pod template requests nothing for is skipped, so nothing is reported
//     for object-count quotas (count/deployments.apps, services, ...).
//   - A resource missing from status.used is skipped: the quota controller hasn't reported on it
//     yet, so its free capacity is unknown rather than equal to hard.
//   - Only an actual shortfall is returned; a quota with room produces no entry.
//
// status.used is namespace-wide and shared with every other workload in the namespace, and quota
// headroom says nothing about whether a node can fit the Pod -- callers must word the output so
// neither is implied.
func quotaRolloutHeadroom(quotas []interface{}, workload map[string]interface{}) quotaHeadroomReport {
	extraPods, podSpec := rolloutPendingPods(workload)
	report := quotaHeadroomReport{ExtraPods: extraPods}
	if extraPods <= 0 || podSpec == nil {
		return report
	}
	pod := &corev1.Pod{Spec: *podSpec}
	requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	limits := resourcehelper.PodLimits(pod, resourcehelper.PodResourcesOptions{})
	for _, q := range quotas {
		quotaObject, ok := q.(map[string]interface{})
		if !ok {
			continue
		}
		var quota corev1.ResourceQuota
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(quotaObject, &quota); err != nil {
			klog.V(3).ErrorS(err, "ignoring unconvertible ResourceQuota in quota headroom check")
			continue
		}
		if len(quota.Spec.Scopes) > 0 || quota.Spec.ScopeSelector != nil {
			continue
		}
		var shortfalls []quotaShortfall
		for _, name := range sortedResourceNames(quota.Status.Hard) {
			used, reported := quota.Status.Used[name]
			if !reported {
				continue
			}
			perPod, consumed := quotaResourcePerPod(name, requests, limits)
			if !consumed {
				continue
			}
			need := multiplyQuantity(perPod, extraPods)
			hard := quota.Status.Hard[name]
			free := hard.DeepCopy()
			free.Sub(used)
			if free.Sign() < 0 {
				free.Set(0)
			}
			if need.Cmp(free) <= 0 {
				continue
			}
			shortfalls = append(shortfalls, quotaShortfall{
				Resource: string(name),
				Need:     need.String(),
				Free:     free.String(),
				Used:     used.String(),
				Hard:     hard.String(),
			})
		}
		if len(shortfalls) > 0 {
			report.Quotas = append(report.Quotas, quotaHeadroomQuota{Name: quota.Name, Shortfalls: shortfalls})
		}
	}
	return report
}

// rolloutPendingPods is how many Pods the workload's controller still has to create before its
// rollout is complete -- the replica count it may run at once minus the Pods it already has --
// along with the Pod spec each of them will be created from. A Deployment's allowance includes
// rolling-update surge, resolved with the same intstr helper the Deployment controller's
// ResolveFenceposts uses, since surge is what makes a rollout need more quota than the steady
// state. A StatefulSet gets no surge allowance -- it deletes a Pod before creating its replacement
// -- so for one only a scale-up shows up here. Returns 0 and a nil spec for any other kind, or
// when the object doesn't convert to its API type (a hand-written manifest rendered with -f can be
// missing or mistype anything).
func rolloutPendingPods(workload map[string]interface{}) (int, *corev1.PodSpec) {
	switch cast.ToString(workload["kind"]) {
	case "Deployment":
		var deployment appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(workload, &deployment); err != nil {
			klog.V(3).ErrorS(err, "ignoring unconvertible Deployment in quota headroom check")
			return 0, nil
		}
		desired := 1
		if deployment.Spec.Replicas != nil {
			desired = int(*deployment.Spec.Replicas)
		}
		surge := 0
		if strategy := deployment.Spec.Strategy; strategy.Type == "" || strategy.Type == appsv1.RollingUpdateDeploymentStrategyType {
			maxSurge := intstr.FromString("25%")
			if strategy.RollingUpdate != nil && strategy.RollingUpdate.MaxSurge != nil {
				maxSurge = *strategy.RollingUpdate.MaxSurge
			}
			surge, _ = intstr.GetScaledValueFromIntOrPercent(&maxSurge, desired, true)
		}
		return desired + surge - int(deployment.Status.Replicas), &deployment.Spec.Template.Spec
	case "StatefulSet":
		var statefulSet appsv1.StatefulSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(workload, &statefulSet); err != nil {
			klog.V(3).ErrorS(err, "ignoring unconvertible StatefulSet in quota headroom check")
			return 0, nil
		}
		desired := 1
		if statefulSet.Spec.Replicas != nil {
			desired = int(*statefulSet.Spec.Replicas)
		}
		return desired - int(statefulSet.Status.Replicas), &statefulSet.Spec.Template.Spec
	}
	return 0, nil
}

// resourceLimitsPrefix is the "limits.<resource>" counterpart of core/v1's
// DefaultResourceRequestsPrefix, which upstream declares only for the requests form.
const resourceLimitsPrefix = "limits."

// quotaResourcePerPod maps a ResourceQuota resource name to how much of it a single Pod of the
// template consumes, following the quota resource naming: "requests.<name>"/"limits.<name>", the
// bare "cpu"/"memory" aliases for the requests form, and "pods" for the Pod count itself. consumed
// is false for a resource the Pod template doesn't ask for, including object counts.
func quotaResourcePerPod(quotaResource corev1.ResourceName, requests, limits corev1.ResourceList) (quantity resource2.Quantity, consumed bool) {
	if quotaResource == corev1.ResourcePods {
		return *resource2.NewQuantity(1, resource2.DecimalSI), true
	}
	amounts, name := requests, string(quotaResource)
	switch {
	case strings.HasPrefix(name, corev1.DefaultResourceRequestsPrefix):
		name = strings.TrimPrefix(name, corev1.DefaultResourceRequestsPrefix)
	case strings.HasPrefix(name, resourceLimitsPrefix):
		amounts, name = limits, strings.TrimPrefix(name, resourceLimitsPrefix)
	}
	perPod, ok := amounts[corev1.ResourceName(name)]
	if !ok || perPod.IsZero() {
		return resource2.Quantity{}, false
	}
	return perPod, true
}

// multiplyQuantity returns quantity added to itself count times, keeping it a resource.Quantity
// (which has no multiplication of its own) so the result stays exact and prints in the same
// canonical form as the quota's own values. Pod counts here are small.
func multiplyQuantity(quantity resource2.Quantity, count int) resource2.Quantity {
	total := quantity.DeepCopy()
	for i := 1; i < count; i++ {
		total.Add(quantity)
	}
	return total
}

func sortedResourceNames(list corev1.ResourceList) []corev1.ResourceName {
	names := make([]corev1.ResourceName, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}
