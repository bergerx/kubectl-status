package plugin

import (
	"encoding/json"
	"github.com/Masterminds/sprig/v3"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/resource"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"os"
	"syscall"
	"text/template"
	"time"
)

var funcMap = template.FuncMap{
	"green":  color.GreenString,
	"greenBold":  color.New(color.FgGreen, color.Bold).SprintfFunc(),
	"yellow": color.YellowString,
	"yellowBold": color.New(color.FgYellow, color.Bold).SprintfFunc(),
	"red": color.RedString,
	"redBold": color.New(color.FgRed, color.Bold).SprintfFunc(),
	"cyan": color.CyanString,
	"cyanBold": color.HiCyanString,
	"bold": color.New(color.Bold).SprintfFunc(),
	"colorAgo": colorAgo,
	"colorDuration": colorDuration,
	"dateSub": func (date1, date2 time.Time) time.Duration { return date2.Sub(date1) },
	"podQosColor": podQosColor,
	"podPhaseColor": podPhaseColor,
	"signalName": func (signal int64) string { return unix.SignalName(syscall.Signal(signal)) },
	"redIf": func (cond bool, str string) string { if cond { return color.RedString(str) }; return str},
	/*
	containerReadyColor
	containerRestartCountColor
	containerExitCodeColor
	containerTerminateReasonColor
	  'red' if reason in ('Error', 'OOMKilled') else 'green'
	PodReasonColor:
	  'red' if 'Evicted'
	podPhaseColor
	markRed "latest" --> image:tag, paint "latest" in red
	exitCodeColor
	toSignalName
	humanizeBytes
	*/
}
func podQosColor(qos string) string {
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

func podPhaseColor(phase string) string {
	switch phase {
	case "Pending":
		return color.YellowString(phase)
	case "Running":
		return color.GreenString(phase)
	case "Succeeded":
		return color.GreenString(phase)
	case "Failed":
		return color.RedString(phase)
	case "Unknown":
		return color.RedString(phase)
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
	if duration < time.Minute * 5 {
		return color.RedString(str)
	}
	if duration < time.Hour {
		return color.YellowString(str)
	}
	if duration < time.Hour * 24 {
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

	allErrs := []error{}
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
