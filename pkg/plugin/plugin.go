//go:generate statik -src templates/
package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
	_ "unsafe"

	"github.com/Masterminds/sprig/v3"
	_ "github.com/bergerx/kubectl-status/pkg/plugin/statik"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/pkg/errors"
	"github.com/pmezard/go-difflib/difflib"
	sfs "github.com/rakyll/statik/fs"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/api/extensions/v1beta1"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/kubernetes"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/scheme"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	kyaml "sigs.k8s.io/yaml"
)

type IngressBackendIssue struct {
	IssueType string
	Backend   v1beta1.IngressBackend
}

var durationRound = (sprig.GenericFuncMap()["durationRound"]).(func(duration interface{}) string)

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
	"markGreen":             markGreen,
	"redIf":                 redIf,
	"redBoldIf":             redBoldIf,
	"signalName":            signalName,
	"isPodConditionHealthy": isPodConditionHealthy,
	"quantityToFloat64":     quantityToFloat64,
	"quantityToInt64":       quantityToInt64,
	"percent":               percent,
	"humanizeSI":            humanizeSI,
	"getItemInList":         getItemInList,
}

func humanizeSI(unit string, input float64) string {
	return humanize.SIWithDigits(input, 1, unit)
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

func RunPlugin(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	//log := logger.NewLogger()
	//log.Info(strings.Join(args, ","))
	clientSet, _ := f.KubernetesClientSet()
	clientConfig := f.ToRawKubeConfigLoader()
	allNamespaces := cmdutil.GetFlagBool(cmd, "all-namespaces")
	namespace, enforceNamespace, err := clientConfig.Namespace()
	if err != nil {
		return errors.WithMessage(err, "Failed getting namespace")
	}
	filenames := cmdutil.GetFlagStringSlice(cmd, "filename")
	isTest := cmdutil.GetFlagBool(cmd, "test")
	if isTest {
		if len(filenames) != 1 {
			return errors.New("When using --test, exactly one --filename must be provided.")
		}
		filename := filenames[0]
		out, err := renderFile(filename)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	}

	r := f.NewBuilder().
		Unstructured().
		NamespaceParam(namespace).DefaultNamespace().AllNamespaces(allNamespaces).
		FilenameParam(enforceNamespace, &resource.FilenameOptions{Filenames: filenames}).
		LabelSelectorParam(cmdutil.GetFlagString(cmd, "selector")).
		FieldSelectorParam(cmdutil.GetFlagString(cmd, "field-selector")).
		ResourceTypeOrNameArgs(true, args...).
		ContinueOnError().
		Latest().
		Flatten().
		Do()

	err = r.Err()
	if err != nil {
		return errors.WithMessage(err, "Failed during querying of resources")
	}

	templateText, err := getTemplate()
	if err != nil {
		return err
	}

	var allErrs []error
	infos, err := r.Infos()
	if err != nil {
		allErrs = append(allErrs, err)
	}
	if len(infos) == 0 {
		if !allNamespaces && namespace != "" {
			fmt.Printf("No resources found in %s namespace\n", namespace)
		} else {
			fmt.Printf("No resources found.\n")
		}
	}
	for _, info := range infos {
		var err error
		out := map[string]interface{}{}
		obj := info.Object
		objKind := info.ResourceMapping().GroupVersionKind.Kind
		err = includeObj(obj, out)
		if err != nil {
			allErrs = append(allErrs, err)
			continue
		}
		err = includeEvents(obj, clientSet, out)
		if err != nil {
			allErrs = append(allErrs, err)
			continue
		}
		kindInjectFuncMap := map[string][]func(obj runtime.Object, f cmdutil.Factory, out map[string]interface{}) error{
			"Node":        {includeNodeMetrics},
			"Pod":         {includePodMetrics},
			"Service":     {includeEndpoint},
			"StatefulSet": {includeStatefulSetDiff},
			"Ingress":     {includeIngressServices},
		}
		for kind, funcs := range kindInjectFuncMap {
			if objKind == kind {
				for _, fu := range funcs {
					err = fu(obj, f, out)
					if err != nil {
						allErrs = append(allErrs, err)
						continue
					}
				}
			}
		}

		err = renderTemplate(templateText, os.Stdout, out)
		if err != nil {
			allErrs = append(allErrs, err)
			continue
		}
		// Add a newline at the end of every template
		fmt.Println("")
	}
	return utilerrors.NewAggregate(allErrs)
}

func renderFile(manifestFilename string) (string, error) {
	var out map[string]interface{}
	manifestFile, _ := ioutil.ReadFile(manifestFilename)
	err := kyaml.Unmarshal(manifestFile, &out)
	if err != nil {
		return "", errors.WithMessage(err, "Failed getting JSON for object")
	}
	templateText, _ := getTemplate()
	var output bytes.Buffer
	err = renderTemplate(templateText, &output, out)
	if err != nil {
		return "", err
	}
	return output.String(), nil
}

func renderTemplate(templateText string, wr io.Writer, v map[string]interface{}) error {
	tmpl, err := template.
		New("templates.tmpl").
		Funcs(sprig.TxtFuncMap()).
		Funcs(funcMap).
		Parse(templateText)
	if err != nil {
		return err
	}
	kindTemplateName := findTemplateName(tmpl, v)
	return tmpl.ExecuteTemplate(wr, kindTemplateName, v)
}

func findTemplateName(tmpl *template.Template, v map[string]interface{}) string {
	objKind := v["kind"].(string)
	var kindTemplateName string
	if t := tmpl.Lookup(objKind); t != nil {
		kindTemplateName = objKind
	} else {
		kindTemplateName = "DefaultResource"
	}
	return kindTemplateName
}

func includeObj(obj runtime.Object, out map[string]interface{}) error {
	return unmarshal(obj, &out)
}

func unmarshal(v interface{}, out *map[string]interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, out)
	if err != nil {
		return err
	}
	return nil
}

func includeEvents(obj runtime.Object, clientSet *kubernetes.Clientset, out map[string]interface{}) error {
	objectMeta := obj.(metav1.Object)
	events, err := clientSet.CoreV1().Events(objectMeta.GetNamespace()).Search(scheme.Scheme, obj)
	if err != nil {
		return errors.WithMessage(err, "Failed getting event")
	}
	eventsKey := make(map[string]interface{})
	err = unmarshal(events, &eventsKey)
	if err != nil {
		return errors.WithMessage(err, "Failed getting JSON for Events")
	}
	out["events"] = eventsKey
	return nil
}

func includeNodeMetrics(obj runtime.Object, f cmdutil.Factory, out map[string]interface{}) error {
	config, _ := f.ToRESTConfig()
	clientSet, err := metricsv.NewForConfig(config)
	if err != nil {
		return errors.WithMessage(err, "Failed getting metrics clientSet")
	}
	objectMeta := obj.(metav1.Object)
	nodeMetrics, err := clientSet.MetricsV1beta1().
		NodeMetricses().
		Get(objectMeta.GetName(), metav1.GetOptions{})
	if err != nil {
		// swallow any errors while getting NodeMetrics
		return nil
	}
	nodeMetricsKey := make(map[string]interface{})
	err = unmarshal(nodeMetrics, &nodeMetricsKey)
	if err != nil {
		return errors.WithMessage(err, "Failed getting JSON for NodeMetrics")
	}
	out["nodeMetrics"] = nodeMetricsKey
	return nil
}

func includePodMetrics(obj runtime.Object, f cmdutil.Factory, out map[string]interface{}) error {
	config, _ := f.ToRESTConfig()
	clientSet, err := metricsv.NewForConfig(config)
	if err != nil {
		return errors.WithMessage(err, "Failed getting metrics clientSet")
	}
	objectMeta := obj.(metav1.Object)
	podMetrics, err := clientSet.MetricsV1beta1().
		PodMetricses(objectMeta.GetNamespace()).
		Get(objectMeta.GetName(), metav1.GetOptions{})
	if err != nil {
		// swallow any errors while getting PodMetrics
		return nil
	}
	podMetricsKey := make(map[string]interface{})
	err = unmarshal(podMetrics, &podMetricsKey)
	if err != nil {
		return errors.WithMessage(err, "Failed getting JSON for PodMetrics")
	}
	out["podMetrics"] = podMetricsKey
	return nil
}

func includeEndpoint(obj runtime.Object, f cmdutil.Factory, out map[string]interface{}) error {
	clientSet, _ := f.KubernetesClientSet()
	objectMeta := obj.(metav1.Object)
	endpoint, err := clientSet.CoreV1().
		Endpoints(objectMeta.GetNamespace()).
		Get(objectMeta.GetName(), metav1.GetOptions{})
	if err != nil {
		return errors.WithMessage(err, "Failed getting Endpoint")
	}
	endpointKey := make(map[string]interface{})
	err = unmarshal(endpoint, &endpointKey)
	if err != nil {
		return errors.WithMessage(err, "Failed getting JSON for Endpoint")
	}
	out["endpoint"] = endpointKey
	return nil
}

func includeIngressServices(obj runtime.Object, f cmdutil.Factory, out map[string]interface{}) error {
	clientSet, _ := f.KubernetesClientSet()
	ing := &v1beta1.Ingress{}
	err := scheme.Scheme.Convert(obj, ing, nil)
	if err != nil {
		return err
	}
	var backendIssues []IngressBackendIssue
	for _, rule := range ing.Spec.Rules {
	PATH:
		for _, path := range rule.HTTP.Paths {
			backend := path.Backend
			svcName := backend.ServiceName
			svcPort := backend.ServicePort
			for _, issue := range backendIssues {
				if issue.Backend.ServiceName == svcName &&
					issue.Backend.ServicePort == svcPort {
					continue PATH
				}
			}
			svc, err := clientSet.CoreV1().Services(ing.Namespace).Get(svcName, metav1.GetOptions{})
			if (err != nil) || (svc.Name == "") {
				backendIssues = append(backendIssues, IngressBackendIssue{"serviceMissing", backend})
				continue PATH
			}
			portExist := false
			for _, port := range svc.Spec.Ports {
				if (svcPort.IntVal == port.Port) || (svcPort.StrVal == port.Name) {
					portExist = true
				}
			}
			if !portExist {
				backendIssues = append(backendIssues, IngressBackendIssue{"serviceWithPortMismatch", backend})
				continue PATH
			}
			endpoint, err := clientSet.CoreV1().Endpoints(ing.Namespace).Get(svcName, metav1.GetOptions{})
			if (err != nil) || (endpoint.Name == "") || (len(endpoint.Subsets) == 0) {
				backendIssues = append(backendIssues, IngressBackendIssue{"serviceWithNoReadyAddresses", backend})
				continue PATH
			}
			for _, subset := range endpoint.Subsets {
				if len(subset.Addresses) == 0 {
					backendIssues = append(backendIssues, IngressBackendIssue{"serviceWithNoReadyAddresses", backend})
					continue PATH
				}
			}
		}
	}
	out["backendIssues"] = backendIssues
	return nil
}

func includeStatefulSetDiff(obj runtime.Object, f cmdutil.Factory, out map[string]interface{}) error {
	sts := &v1.StatefulSet{}
	err := scheme.Scheme.Convert(obj, sts, nil)
	if err != nil {
		return errors.WithMessage(err, "StatefulSet object conversion failed")
	}

	if sts.Status.UpdateRevision != "" && sts.Status.CurrentRevision == sts.Status.UpdateRevision {
		// revision details are needed only when they differ
		return nil
	}

	config, _ := f.ToRESTConfig()
	clientSet, err := appsv1.NewForConfig(config)
	if err != nil {
		return errors.WithMessage(err, "Failed getting apps/v1 client")
	}

	currentRevision, err := clientSet.ControllerRevisions(sts.GetNamespace()).
		Get(sts.Status.CurrentRevision, metav1.GetOptions{})
	if err != nil {
		return err
	}
	currentBytes, err := json.MarshalIndent(currentRevision.Data, "", "  ")
	if err != nil {
		return err
	}

	updateRevision, err := clientSet.ControllerRevisions(sts.GetNamespace()).
		Get(sts.Status.UpdateRevision, metav1.GetOptions{})
	if err != nil {
		return err
	}
	updateBytes, err := json.MarshalIndent(updateRevision.Data, "", "  ")
	if err != nil {
		return err
	}
	currentTime := currentRevision.ObjectMeta.GetCreationTimestamp().Time
	updateTime := updateRevision.ObjectMeta.GetCreationTimestamp().Time

	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(currentBytes)),
		B:        difflib.SplitLines(string(updateBytes)),
		FromFile: fmt.Sprintf("currentRevision ControllerRevision/%s", sts.Status.CurrentRevision),
		FromDate: fmt.Sprintf("%s (%s ago)", currentTime.String(), ago(currentTime)),
		ToFile:   fmt.Sprintf("updateRevision  ControllerRevision/%s", sts.Status.UpdateRevision),
		ToDate:   fmt.Sprintf("%s (%s ago)", updateTime.String(), ago(updateTime)),
		Context:  3,
	}
	diffString, _ := difflib.GetUnifiedDiffString(diff)

	out["diff"] = diffString

	return nil
}

func getTemplate() (string, error) {
	statikFS, err := sfs.New()
	if err != nil {
		return "", errors.WithMessage(err, "Failed initiating statikFS")
	}

	// Access individual files by their paths.
	templatesFile := "/templates.tmpl"
	t, err := statikFS.Open(templatesFile)
	if err != nil {
		return "", errors.WithMessage(err, "Failed opening template from statikFS")
	}
	defer t.Close()

	contents, err := ioutil.ReadAll(t)
	if err != nil {
		return "", errors.WithMessage(err, "Failed reading template from statikFS")
	}
	return string(contents), nil
}
