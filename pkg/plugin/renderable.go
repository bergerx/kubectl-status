package plugin

import (
	"bytes"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

func newRenderableObject(obj map[string]interface{}, engine renderEngine) RenderableObject {
	r := RenderableObject{
		Unstructured: unstructured.Unstructured{Object: obj},
		engine:       &engine,
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

func (r RenderableObject) newRenderableObject(obj map[string]interface{}) RenderableObject {
	return newRenderableObject(obj, *r.engine)
}

func (r RenderableObject) options() *Options {
	return &r.engine.Options
}

func (r RenderableObject) String() string {
	kindAndName := fmt.Sprintf("%s/%s", r.Kind(), r.Name())
	if namespace := r.Namespace(); namespace != "" {
		kindAndName = fmt.Sprintf("%s[%s]", kindAndName, namespace)

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

func (r RenderableObject) render(wr io.Writer) error {
	klog.V(5).InfoS("called render, calling findTemplateName", "r", r)
	templateName := findTemplateName(r.engine.Template, r.Kind())
	klog.V(5).InfoS("found template, calling injectExtras", "r", r, "templateName", templateName)
	//err := r.injectExtras()
	//if err != nil {
	//	klog.V(3).ErrorS(err, "ignoring error while injecting arbitrary extras", "r", r)
	//}
	klog.V(5).InfoS("calling executeTemplate on renderable", "r", r, "templateName", templateName)
	err := r.engine.ExecuteTemplate(wr, templateName, r)
	if err != nil {
		klog.V(3).ErrorS(err, "error on executeTemplate", "r", r)
	}
	return err
}

func (r RenderableObject) renderString() (string, error) {
	klog.V(5).InfoS("called renderString", "r", r)
	var buffer bytes.Buffer
	err := r.render(&buffer)
	return buffer.String(), err
}

func (r RenderableObject) renderTemplate(templateName string, data interface{}) (string, error) {
	var buffer bytes.Buffer
	klog.V(5).InfoS("called renderTemplate, calling ExecuteTemplate",
		"r", r, "templateName", templateName, "data", data)
	err := r.engine.ExecuteTemplate(&buffer, templateName, data)
	if err != nil {
		klog.V(3).ErrorS(err, "error executing template",
			"r", r, "templateName", templateName)
	}
	return buffer.String(), err
}
