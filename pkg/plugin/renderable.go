package plugin

import (
	"bytes"
	"fmt"
	"io"

	"github.com/fatih/color"
	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"
)

func newRenderableObject(obj map[string]interface{}, engine *renderEngine) RenderableObject {
	r := RenderableObject{
		Unstructured: unstructured.Unstructured{Object: obj},
		engine:       engine,
		Config:       viper.GetViper(),
	}
	return r
}

// RenderableObject is the object passed to the templates, also provides methods to run queries against Kubernetes API.
// It is an unstructured.Unstructured (so it has the Object field that keeps the Object) but there a numerous helper
// methods that already helps with the templates.
type RenderableObject struct {
	unstructured.Unstructured
	engine *renderEngine
	Config *viper.Viper
}

// KStatus return a Result object of kstatus for the object.
func (r RenderableObject) KStatus() *kstatus.Result {
	result, err := kstatus.Compute(&r.Unstructured)
	if err != nil {
		klog.V(2).ErrorS(err, "kstatus.Compute failed", "r", r)
	}
	return result
}

func (e *renderEngine) newRenderableObject(obj map[string]interface{}) RenderableObject {
	return newRenderableObject(obj, e)
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

func (r RenderableObject) render(wr io.Writer) error {
	klog.V(5).InfoS("called render, calling findTemplateName", "r", r)
	templateName := findTemplateName(r.engine.Template, r.Kind())
	klog.V(5).InfoS("calling executeTemplate on renderable", "r", r, "templateName", templateName)
	err := r.executeTemplate(wr, templateName, r)
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
	err := r.executeTemplate(&buffer, templateName, data)
	if err != nil {
		klog.V(3).ErrorS(err, "error executing template",
			"r", r, "templateName", templateName)
	}
	return buffer.String(), err
}

func (r RenderableObject) executeTemplate(wr io.Writer, name string, data any) error {
	target, ok := data.(RenderableObject)
	if ok && target.Kind() == name && renderedUIDs.checkAdd(target.GetUID()) && !viper.GetBool("watching") && !viper.GetBool("test") {
		klog.V(3).InfoS("skip rendering of the RenderableObject as its already rendered",
			"r", r, "templateName", name)
		_, _ = color.New(color.FgWhite).Fprintf(wr, "%s is already printed", target.String())
		return nil
	}
	return r.engine.ExecuteTemplate(wr, name, data)
}

type uidSet map[types.UID]struct{}

func (s uidSet) checkAdd(uid types.UID) bool {
	_, exists := s[uid]
	if !exists {
		s[uid] = struct{}{}
	}
	return exists
}

var renderedUIDs = make(uidSet)
