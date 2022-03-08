package plugin

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/spf13/cast"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

var durationRound = (sprig.GenericFuncMap()["durationRound"]).(func(duration interface{}) string)

func funcMap() template.FuncMap {
	return template.FuncMap{
		"green":                    color.GreenString,
		"yellow":                   color.YellowString,
		"red":                      color.RedString,
		"cyan":                     color.CyanString,
		"blue":                     color.BlueString,
		"bold":                     color.New(color.Bold).SprintfFunc(),
		"colorAgo":                 colorAgo,
		"colorDuration":            colorDuration,
		"colorBool":                colorBool,
		"colorKeyword":             colorKeyword,
		"markRed":                  markRed,
		"markYellow":               markYellow,
		"markGreen":                markGreen,
		"redIf":                    redIf,
		"redBoldIf":                redBoldIf,
		"signalName":               signalName,
		"isStatusConditionHealthy": isStatusConditionHealthy,
		"quantityToFloat64":        quantityToFloat64,
		"quantityToInt64":          quantityToInt64,
		"percent":                  percent,
		"colorPercent":             colorPercent,
		"humanizeSI":               humanizeSI,
		"getMatchingItemInMapList": getMatchingItemInMapList,
		"sortMapListByKeysValue":   sortMapListByKeysValue,
		"addFloat64":               addFloat64,
		"subFloat64":               subFloat64,
		"divFloat64":               divFloat64,
	}
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
		return color.New(color.FgRed, color.Bold).Sprintf(str)
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
	for i := range mapList {
		result = append(result, mapList[i])
	}
	sort.Slice(result, func(i, j int) bool {
		typedMapListItemI := result[i].(map[string]interface{})[key].(string)
		typedMapListItemJ := result[j].(map[string]interface{})[key].(string)
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

func signalName(signal int64) string {
	return signame(uint32(signal))
}

func redIf(cond interface{}, str string) string {
	if !reflect.ValueOf(cond).IsZero() {
		return color.RedString(str)
	}
	return str
}

func redBoldIf(cond interface{}, str string) string {
	if !reflect.ValueOf(cond).IsZero() {
		return color.New(color.FgRed, color.Bold).Sprintf(str)
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
		return color.New(color.FgRed, color.Bold).Sprintf(phase)
	default:
		return phase
	}
}

func colorAgo(kubeDate string) string {
	t, _ := time.ParseInLocation("2006-01-02T15:04:05Z", kubeDate, time.UTC)
	duration := time.Since(t).Round(time.Second)
	return colorDuration(duration)
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

func (r RenderableObject) Include(templateName string, data interface{}) (string, error) {
	klog.V(5).InfoS("Include", "r", r, "templateName", templateName, "data", data)
	return r.renderTemplate(templateName, data)
}

func (r RenderableObject) IncludeRenderableObject(obj RenderableObject) (output string) {
	klog.V(5).InfoS("called IncludeRenderableObject", "r", r, "obj", obj)
	renderString, _ := obj.renderString()
	return renderString
}
