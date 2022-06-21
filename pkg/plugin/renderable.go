package plugin

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"
)

func newRenderableObject(obj map[string]interface{}, engine *renderEngine) RenderableObject {
	r := RenderableObject{
		Unstructured: unstructured.Unstructured{Object: obj},
		engine:       engine,
	}
	return r
}

// RenderableObject is the object passed to the templates, also provides methods to run queries against Kubernetes API.
// It is an unstructured.Unstructured (so it has the Object field that keeps the Object) but there a numerous helper
// methods that already helps with the templates.
type RenderableObject struct {
	unstructured.Unstructured
	engine *renderEngine
}

// KStatus return a Result object of kstatus for the object.
func (r RenderableObject) KStatus() *kstatus.Result {
	result, err := kstatus.Compute(&r.Unstructured)
	if err != nil {
		klog.V(2).ErrorS(err, "kstatus.Compute failed", "r", r)
	}
	return result
}

func (r RenderableObject) newRenderableObject(obj map[string]interface{}) RenderableObject {
	return newRenderableObject(obj, r.engine)
}

func (r RenderableObject) newIndentedRenderableObject(indent int, obj map[string]interface{}) RenderableObject {
	newEngine, _ := newRenderEngine(r.engine.Options, r.engine.Output, indent, &r.engine.Template)
	return newRenderableObject(obj, newEngine)
}

func (r RenderableObject) String() string {
	kindAndName := fmt.Sprintf("%s/%s", r.Kind(), r.Name())
	if namespace := r.Namespace(); namespace != "" {
		kindAndName = fmt.Sprintf("%s[%s]", kindAndName, namespace)
	}
	if r.engine.indent > 0 {
		kindAndName = fmt.Sprintf("%s[indent=%d]", kindAndName, r.engine.indent)
	}
	return kindAndName
}

func (r RenderableObject) Kind() (kind string) {
	if x := r.Object["kind"]; x != nil {
		kind = x.(string)
	}
	return
}

func (r RenderableObject) Spec() (spec map[string]interface{}) {
	if x := r.Object["spec"]; x != nil {
		spec = x.(map[string]interface{})
	}
	return
}

func (r RenderableObject) Status() (status map[string]interface{}) {
	if x := r.Object["status"]; x != nil {
		status = x.(map[string]interface{})
	}
	return
}

func (r RenderableObject) Metadata() (metadata map[string]interface{}) {
	if x := r.Object["metadata"]; x != nil {
		metadata = x.(map[string]interface{})
	}
	return
}

func (r RenderableObject) Annotations() (annotations map[string]interface{}) {
	if x := r.Metadata()["annotations"]; x != nil {
		annotations = x.(map[string]interface{})
	}
	return
}

func (r RenderableObject) Labels() (labels map[string]interface{}) {
	if x := r.Metadata()["labels"]; x != nil {
		labels = x.(map[string]interface{})
	}
	return
}

func (r RenderableObject) Name() string {
	return r.GetName()
}

func (r RenderableObject) Namespace() string {
	return r.GetNamespace()
}

func (r RenderableObject) StatusConditions() (conditions []interface{}) {
	if x := r.Status()["conditions"]; x != nil {
		conditions = x.([]interface{})
	}
	return
}

func (r RenderableObject) RenderOptions() *RenderOptions {
	return r.engine.RenderOptions
}

func (r RenderableObject) render() error {
	klog.V(5).InfoS("called render, calling findTemplateName", "r", r)
	templateName := findTemplateName(r.engine.Template, r.Kind())
	return r.renderTemplate(templateName, r)
}

func (r RenderableObject) renderTemplate(templateName string, data interface{}) error {
	klog.V(5).InfoS("calling executeTemplate on renderable", "r", r, "templateName", templateName)
	err := r.engine.ExecuteTemplate(r.engine.Output, templateName, data)
	if err != nil {
		klog.V(3).ErrorS(err, "error on executeTemplate", "r", r)
	}
	return err
}
