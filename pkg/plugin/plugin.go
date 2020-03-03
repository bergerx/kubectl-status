package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/resource"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

var funcMap = template.FuncMap{
	"green":                         color.GreenString,
	"greenBold":                     color.New(color.FgGreen, color.Bold).SprintfFunc(),
	"yellow":                        color.YellowString,
	"yellowBold":                    color.New(color.FgYellow, color.Bold).SprintfFunc(),
	"red":                           color.RedString,
	"redBold":                       color.New(color.FgRed, color.Bold).SprintfFunc(),
	"cyan":                          color.CyanString,
	"cyanBold":                      color.HiCyanString,
	"bold":                          color.New(color.Bold).SprintfFunc(),
	"colorAgo":                      colorAgo,
	"colorDuration":                 colorDuration,
	"markRed":                       markRed,
	"markYellow":                    markYellow,
	"redIf":                         redIf,
	"dateSub":                       dateSub,
	"conditionStatusColor":          conditionStatusColor,
	"colorPodQos":                   colorPodQos,
	"colorPodPhase":                 colorPodPhase,
	"colorPodPReason":               colorPodReason,
	"colorContainerTerminateReason": colorContainerTerminateReason,
	"colorExitCode":                 colorExitCode,
	"signalName":                    signalName,
	// "colorContainerReady": colorContainerReady,

	/*
		humanizeBytes
	*/
}

func conditionStatusColor(condition map[string]interface{}, str string) string {
	switch {
	case
	strings.HasSuffix(fmt.Sprint(condition["type"]), "Pressure"), // Node Pressure conditions
	condition["type"] == "Failed":  // Failed Jobs has this condition
		switch condition["status"] {
		case "False":
			return str
		case "True", "Unknown":
			return color.New(color.FgRed, color.Bold).Sprintf(str)
		default:
			return color.New(color.FgRed, color.Bold).Sprintf(str)
		}
	default:
		switch condition["status"] {
		case "True":
			return str
		case "False", "Unknown":
			return color.New(color.FgRed, color.Bold).Sprintf(str)
		default:
			return color.New(color.FgRed, color.Bold).Sprintf(str)
		}
	}
}

func colorContainerTerminateReason(reason string) string {
	switch reason {
	case "OOMKilled", "ContainerCannotRun", "Error":
		return color.New(color.FgRed, color.Bold).Sprint(reason)
	case "Completed":
		return color.GreenString(reason)
	default:
		return reason
	}
}

func dateSub(date1, date2 time.Time) time.Duration {
	return date2.Sub(date1)
}

func signalName(signal int64) string {
	return unix.SignalName(syscall.Signal(signal))
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

func colorPodQos(qos string) string {
	switch qos {
	case "BestEffort":
		return color.RedString(qos)
	case "Burstable":
		return color.YellowString(qos)
	case "Guaranteed":
		return color.GreenString(qos)
	default:
		return color.RedString(qos)
	}
}

func colorPodPhase(phase string) string {
	switch phase {
	case "Pending":
		return color.YellowString(phase)
	case "Running", "Succeeded":
		return color.GreenString(phase)
	case "Failed", "Unknown":
		return color.RedString(phase)
	default:
		return phase
	}
}
func colorPodReason(reason string) string {
	switch reason {
	case "Evicted":
		return color.RedString(reason)
	default:
		return reason
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
			New("pkg/plugin/templates.tmpl").
			Funcs(sprig.TxtFuncMap()).
			Funcs(funcMap).
			ParseFiles("pkg/plugin/templates.tmpl"))
		kind := info.ResourceMapping().GroupVersionKind.Kind
		err = tmpl.ExecuteTemplate(os.Stderr, kind, out)
		if err != nil {
			if errs.Has(err.Error()) {
				continue
			}
			allErrs = append(allErrs, err)
			errs.Insert(err.Error())
			continue
		}
	}
	return utilerrors.NewAggregate(allErrs)
}
