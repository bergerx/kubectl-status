//go:generate statik -src templates/
package plugin

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/spf13/cast"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubectl/pkg/scheme"
)

var durationRound = (sprig.GenericFuncMap()["durationRound"]).(func(duration interface{}) string)

var funcMap = template.FuncMap{
	"green":                    color.GreenString,
	"yellow":                   color.YellowString,
	"red":                      color.RedString,
	"cyan":                     color.CyanString,
	"bold":                     color.New(color.Bold).SprintfFunc(),
	"colorAgo":                 colorAgo,
	"colorDuration":            colorDuration,
	"colorBool":                colorBool,
	"colorKeyword":             colorKeyword,
	"colorExitCode":            colorExitCode,
	"markRed":                  markRed,
	"markYellow":               markYellow,
	"markGreen":                markGreen,
	"redIf":                    redIf,
	"redBoldIf":                redBoldIf,
	"signalName":               signalName,
	"isPodConditionHealthy":    isPodConditionHealthy,
	"quantityToFloat64":        quantityToFloat64,
	"quantityToInt64":          quantityToInt64,
	"percent":                  percent,
	"colorPercent":             colorPercent,
	"humanizeSI":               humanizeSI,
	"getItemInList":            getItemInList,
	"getPodInNodeStatsSummary": getPodInNodeStatsSummary,
	"addFloat64":               addFloat64,
	"subFloat64":               subFloat64,
	"divFloat64":               divFloat64,
	"include":                  func(string, interface{}) (string, error) { return "include function is not implemented", nil },
	"kubeGet":                  func(...string) interface{} { return "" },
	"kubeGetFirst":             func(...string) interface{} { return "" },
	"getEvents":                func(map[string]interface{}) map[string]interface{} { return nil },
	"includeObj":               func(...string) interface{} { return "" },
	"includeOwners":            func(interface{}) interface{} { return "" },
}

func getFuncMap() template.FuncMap {
	f := sprig.TxtFuncMap()
	for k, v := range funcMap {
		f[k] = v
	}
	return f
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

func getItemInList(list []interface{}, itemKey, itemValue string) map[string]interface{} {
	var item map[string]interface{}
	for _, untypedItem := range list {
		typedItem := untypedItem.(map[string]interface{})
		if typedItem[itemKey].(string) == itemValue {
			item = typedItem
			break
		}
	}
	return item
}

func getPodInNodeStatsSummary(namespace, name string, nodeStatsSummary []interface{}) map[string]interface{} {
	var item map[string]interface{}
	for _, untypedItem := range nodeStatsSummary {
		typedItem := untypedItem.(map[string]interface{})
		podRef := typedItem["podRef"].(map[string]interface{})
		if podRef["namespace"].(string) == namespace && podRef["name"].(string) == name {
			scheme.Scheme.Convert(&typedItem, &item, nil)
			break
		}
	}
	return item
}

func isPodConditionHealthy(condition map[string]interface{}) bool {
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
		condition["type"] == "Failed":                                   // Failed Jobs has this condition
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

func redIf(cond bool, str string) string {
	if cond {
		return color.RedString(str)
	}
	return str
}

func redBoldIf(cond bool, str string) string {
	if cond {
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

func colorExitCode(exitCode int) string {
	switch exitCode {
	case 0:
		return strconv.Itoa(exitCode)
	default:
		return color.RedString("%d", exitCode)
	}
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
	case "Running", "Succeeded", "Active", "Available", "Bound", "valid", "Guaranteed", "Completed":
		return color.GreenString(phase)
	case "Pending", "Released", "Burstable":
		return color.YellowString(phase)
	case "Failed", "Unknown", "Terminating", "Evicted", "BestEffort", "OOMKilled", "ContainerCannotRun", "Error":
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

func include(name string, data interface{}) (string, error) {
	var buf strings.Builder
	tmpl, err := getParsedTemplates()
	if err != nil {
		return "", err
	}
	funcMap := getFuncMap()
	funcMap["include"] = include
	tmpl.Funcs(funcMap)
	err = tmpl.ExecuteTemplate(&buf, name, data)
	return buf.String(), err
}
