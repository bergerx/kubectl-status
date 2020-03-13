//go:generate statik -src templates/
package plugin

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"
	_ "unsafe"

	"github.com/Masterminds/sprig/v3"
	_ "github.com/bergerx/kubectl-status/pkg/plugin/statik"
	"github.com/fatih/color"
	sfs "github.com/rakyll/statik/fs"
	"github.com/spf13/cobra"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/resource"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

var funcMap = template.FuncMap{
	"green":                 color.GreenString,
	"yellow":                color.YellowString,
	"red":                   color.RedString,
	"cyan":                  color.CyanString,
	"bold":                  color.New(color.Bold).SprintfFunc(),
	"colorAgo":              colorAgo,
	"colorDuration":         colorDuration,
	"colorBool":             colorBool,
	"colorKeyword":          colorKeyword,
	"colorExitCode":         colorExitCode,
	"markRed":               markRed,
	"markYellow":            markYellow,
	"redIf":                 redIf,
	"signalName":            signalName,
	"getPodCondition":       getPodCondition,
	"isPodConditionHealthy": isPodConditionHealthy,
}

func colorBool(cond bool, str string) string {
	if cond {
		return color.GreenString(str)
	} else {
		return color.New(color.FgRed, color.Bold).Sprintf(str)
	}
}

func getPodCondition(conditions []interface{}, conditionType string) map[string]interface{} {
	var condition map[string]interface{}
	for _, untypedCondition := range conditions {
		typedCondition := untypedCondition.(map[string]interface{})
		if typedCondition["type"] == conditionType {
			condition = typedCondition
			break
		}
	}
	return condition
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

//go:linkname signame runtime.signame
func signame(sig uint32) string

func signalName(signal int64) string {
	return signame(uint32(signal))
}

func redIf(cond bool, str string) string {
	if cond {
		return color.RedString(str)
	}
	return str
}

func markRed(substr, s string) string {
	return strings.ReplaceAll(s, substr, color.RedString(substr))
}

func markYellow(substr, s string) string {
	return strings.ReplaceAll(s, substr, color.YellowString(substr))
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
	t, _ := time.ParseInLocation("2006-01-02T15:04:05Z", kubeDate, time.Local)
	duration := time.Since(t).Round(time.Second)
	return colorDuration(duration)
}

func colorDuration(duration time.Duration) string {
	durationRound := (sprig.GenericFuncMap()["durationRound"]).(func(duration interface{}) string)
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

func RunPlugin(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	//log := logger.NewLogger()
	//log.Info(strings.Join(args, ","))
	clientConfig := f.ToRawKubeConfigLoader()
	namespace, enforceNamespace, err := clientConfig.Namespace()
	if err != nil {
		return err
	}
	filenames := cmdutil.GetFlagStringSlice(cmd, "filename")

	r := f.NewBuilder().
		Unstructured().
		NamespaceParam(namespace).DefaultNamespace().AllNamespaces(cmdutil.GetFlagBool(cmd, "all-namespaces")).
		FilenameParam(enforceNamespace, &resource.FilenameOptions{Filenames: filenames}).
		LabelSelectorParam(cmdutil.GetFlagString(cmd, "selector")).
		ResourceTypeOrNameArgs(true, args...).
		ContinueOnError().
		Flatten().
		Do()

	err = r.Err()
	if err != nil {
		return err
	}

	contents, err := getTemplate()
	if err != nil {
		return err
	}

	var allErrs []error
	infos, infosErr := r.Infos()
	if infosErr != nil {
		allErrs = append(allErrs, err)
	}
	errs := sets.NewString()
	for _, info := range infos {
		var data []byte
		var err error
		obj := info.Object
		data, err = json.Marshal(obj)
		if err != nil {
			if errs.Has(err.Error()) {
				continue
			}
			allErrs = append(allErrs, err)
			errs.Insert(err.Error())
			continue
		}

		out := map[string]interface{}{}
		if err := json.Unmarshal(data, &out); err != nil {
			if errs.Has(err.Error()) {
				continue
			}
			allErrs = append(allErrs, err)
			errs.Insert(err.Error())
			continue
		}

		tmpl := template.Must(template.
			New("templates.tmpl").
			Funcs(sprig.TxtFuncMap()).
			Funcs(funcMap).
			Parse(contents))
		kind := info.ResourceMapping().GroupVersionKind.Kind
		var kindTemplateName string
		if t := tmpl.Lookup(kind); t != nil {
			kindTemplateName = kind
		} else {
			kindTemplateName = "DefaultResource"
		}
		err = tmpl.ExecuteTemplate(os.Stdout, kindTemplateName, out)
		if err != nil {
			if errs.Has(err.Error()) {
				continue
			}
			allErrs = append(allErrs, err)
			errs.Insert(err.Error())
			continue
		}
		// Add a newline at the end of every template
		fmt.Println("")
	}
	return utilerrors.NewAggregate(allErrs)
}

func getTemplate() (string, error) {
	statikFS, err := sfs.New()
	if err != nil {
		return "", err
	}

	// Access individual files by their paths.
	templatesFile := "/templates.tmpl"
	t, err := statikFS.Open(templatesFile)
	if err != nil {
		return "", err
	}
	defer t.Close()

	contents, err := ioutil.ReadAll(t)
	if err != nil {
		return "", err
	}
	return string(contents), nil
}
