package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/pmezard/go-difflib/difflib"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/scheme"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

type IngressBackendIssue struct {
	IssueType string
	Backend   v1beta1.IngressBackend
}

func includePodMetrics(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error {
	clientSet, err := metricsv.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting metrics clientSet")
	}
	objectMeta := obj.(metav1.Object)
	podMetrics, err := clientSet.MetricsV1beta1().
		PodMetricses(objectMeta.GetNamespace()).
		Get(context.TODO(), objectMeta.GetName(), metav1.GetOptions{})
	if err != nil {
		// swallow any errors while getting PodMetrics
		return nil
	}
	podMetricsKey, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&podMetrics)
	if err != nil {
		return errors.WithMessage(err, "Failed getting JSON for PodMetrics")
	}
	out["podMetrics"] = podMetricsKey
	return nil
}

func includeEndpoint(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error {
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting clientSet")
	}
	objectMeta := obj.(metav1.Object)
	endpoint, err := clientSet.CoreV1().
		Endpoints(objectMeta.GetNamespace()).
		Get(context.TODO(), objectMeta.GetName(), metav1.GetOptions{})
	if err != nil {
		return errors.WithMessage(err, "Failed getting Endpoint")
	}
	endpointKey, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&endpoint)
	if err != nil {
		return errors.WithMessage(err, "Failed getting JSON for Endpoint")
	}
	out["endpoint"] = endpointKey
	return nil
}

func includeNodeLease(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error {
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting clientSet")
	}
	objectMeta := obj.(metav1.Object)
	lease, err := clientSet.CoordinationV1().
		Leases(corev1.NamespaceNodeLease).
		Get(context.TODO(), objectMeta.GetName(), metav1.GetOptions{})
	if err != nil {
		return errors.WithMessage(err, "Failed getting Node Lease")
	}
	leaseKey, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&lease)
	if err != nil {
		return errors.WithMessage(err, "Failed getting JSON for Lease")
	}
	out["lease"] = leaseKey
	return nil
}

func includePodDetailsOnNode(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error {
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting clientSet")
	}
	objectMeta := obj.(metav1.Object)
	fieldSelector, err := fields.ParseSelector("spec.nodeName=" + objectMeta.GetName() +
		",status.phase!=" + string(corev1.PodSucceeded) +
		",status.phase!=" + string(corev1.PodFailed))
	if err != nil {
		return errors.WithMessage(err, "Failed creating fieldSelector for non-terminated Pods on Node")
	}
	nodeNonTerminatedPodsList, err := clientSet.CoreV1().
		Pods(objectMeta.GetNamespace()).
		List(context.TODO(), metav1.ListOptions{FieldSelector: fieldSelector.String()})
	if err != nil {
		return errors.WithMessage(err, "Failed getting non-terminated Pods for Node")
	}
	var podsList []interface{}
	for _, pod := range nodeNonTerminatedPodsList.Items {
		pod.Kind = "Pod"
		podKey, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
		err = includePodMetrics(unstructuredToRuntimeObject(podKey), restConfig, podKey)
		if err != nil {
			return errors.WithMessage(err, "Failed including PodMetrics for Pods for Node")
		}
		podsList = append(podsList, podKey)
	}
	out["pods"] = podsList
	return nil
}

func unstructuredToRuntimeObject(obj map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: obj}
}

func includeNodeStatsSummary(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error {
	// This endpoint will be disabled soon https://github.com/kubernetes/kubernetes/issues/68522
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting clientSet")
	}
	objectMeta := obj.(metav1.Object)
	getBytes, err := clientSet.CoreV1().RESTClient().Get().
		Resource("nodes").
		SubResource("proxy").
		Name(objectMeta.GetName()).
		Suffix("stats/summary").
		DoRaw(context.TODO())
	if err != nil {
		// ignore any errors querying the endpoint
		return nil
	}
	nodeStatsSummary := make(map[string]interface{})
	err = json.Unmarshal(getBytes, &nodeStatsSummary)
	if err != nil {
		return errors.WithMessage(err, "Failed parsing JSON for Node's /stats/summary")
	}
	out["nodeStatsSummary"] = nodeStatsSummary
	return nil
}

func includeIngressServices(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error {
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting clientSet")
	}
	if unsupportedApiVersion := checkUnsupportedIngressApiVersion(obj); unsupportedApiVersion != "" {
		out["unsupportedApiVersion"] = unsupportedApiVersion
		return nil
	}
	ing := &v1beta1.Ingress{}
	err = scheme.Scheme.Convert(obj, ing, nil)
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
			svc, err := clientSet.CoreV1().Services(ing.Namespace).Get(context.TODO(), svcName, metav1.GetOptions{})
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
			endpoint, err := clientSet.CoreV1().Endpoints(ing.Namespace).Get(context.TODO(), svcName, metav1.GetOptions{})
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

func checkUnsupportedIngressApiVersion(obj runtime.Object) string {
	objectGroupVersion := obj.GetObjectKind().GroupVersionKind().GroupVersion().String()
	supportedGroupVersion := v1beta1.SchemeGroupVersion.String()
	if objectGroupVersion != supportedGroupVersion {
		return objectGroupVersion
	}
	return ""
}

func includeStatefulSetDiff(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error {
	sts := &v1.StatefulSet{}
	err := runtimeObjectToSpecificObject(obj, sts)
	if err != nil {
		return errors.WithMessage(err, "StatefulSet object conversion failed")
	}

	if sts.Status.UpdateRevision != "" && sts.Status.CurrentRevision == sts.Status.UpdateRevision {
		// revision details are needed only when they differ
		return nil
	}

	clientSet, err := appsv1.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting apps/v1 client")
	}

	currentRevision, err := clientSet.ControllerRevisions(sts.GetNamespace()).
		Get(context.TODO(), sts.Status.CurrentRevision, metav1.GetOptions{})
	if err != nil {
		return err
	}
	currentBytes, err := json.MarshalIndent(currentRevision.Data, "", "  ")
	if err != nil {
		return err
	}

	updateRevision, err := clientSet.ControllerRevisions(sts.GetNamespace()).
		Get(context.TODO(), sts.Status.UpdateRevision, metav1.GetOptions{})
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

func runtimeObjectToSpecificObject(obj runtime.Object, out interface{}) error {
	return scheme.Scheme.Convert(obj, out, nil)
}
