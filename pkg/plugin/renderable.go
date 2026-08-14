package plugin

import (
	"bytes"
	"fmt"
	"io"
	"text/template"

	"github.com/fatih/color"
	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func newRenderableObject(obj map[string]interface{}, engine *renderEngine, repo *input.ResourceRepo) RenderableObject {
	r := RenderableObject{
		Unstructured: unstructured.Unstructured{Object: sanitizeObject(obj)},
		engine:       engine,
		repo:         repo,
		Config:       engine.cfg.Viper,
	}
	return r
}

// RenderableObject is the object passed to the templates, also provides methods to run queries against Kubernetes API.
// It is an unstructured.Unstructured (so it has the Object field that keeps the Object) but there a numerous helper
// methods that already helps with the templates.
type RenderableObject struct {
	unstructured.Unstructured
	engine *renderEngine
	repo   *input.ResourceRepo
	Config *viper.Viper
	// currentTree is the *template.Template this object's own top-level render() dispatched
	// to (embedded or the user overlay tree -- see templateSet). It starts nil (set only once
	// render() runs) and is what executeTemplate/resolveIncludeTree consult so that an
	// `.Include`/`$.Include` call for an internal helper name made while rendering this object
	// resolves within that same tree, rather than being able to cross into the other one. See
	// templateSet.resolveIncludeTree's doc comment for the full policy and #809 for why this
	// matters.
	currentTree *template.Template
}

// KStatus return a Result object of kstatus for the object.
func (r RenderableObject) KStatus() *kstatus.Result {
	result, err := kstatus.Compute(&r.Unstructured)
	if err != nil {
		klog.V(2).ErrorS(err, "kstatus.Compute failed", "r", r)
	}
	return result
}

// Problematic reports whether the object's kstatus disagrees with Current -- the same "not
// Current" test kstatus_if_abnormal already uses in templates -- for callers that need it as a
// plain bool rather than rendered text, e.g. deciding whether a matched Pod is worth a full
// inline render even outside --deep. Returns false when kstatus.Compute failed to produce a
// result (rather than treating "unknown" as "problematic").
func (r RenderableObject) Problematic() bool {
	result := r.KStatus()
	return result != nil && result.Status != kstatus.CurrentStatus
}

func (r RenderableObject) newRenderableObject(obj map[string]interface{}) RenderableObject {
	return newRenderableObject(obj, r.engine, r.repo)
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

func (r RenderableObject) APIVersion() (apiVersion string) {
	if x := r.Object["apiVersion"]; x != nil {
		apiVersion = x.(string)
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
		status, _ = x.(map[string]interface{})
	}
	if status == nil {
		status = make(map[string]interface{})
	}
	return
}

func (r RenderableObject) Metadata() (metadata map[string]interface{}) {
	if x := r.Object["metadata"]; x != nil {
		metadata, _ = x.(map[string]interface{})
	}
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	return
}

func (r RenderableObject) Annotations() (annotations map[string]interface{}) {
	if x := r.Metadata()["annotations"]; x != nil {
		annotations, _ = x.(map[string]interface{})
	}
	if annotations == nil {
		annotations = make(map[string]interface{})
	}
	return
}

func (r RenderableObject) Labels() (labels map[string]interface{}) {
	if x := r.Metadata()["labels"]; x != nil {
		labels, _ = x.(map[string]interface{})
	}
	if labels == nil {
		labels = make(map[string]interface{})
	}
	return
}

func (r RenderableObject) Name() string {
	return r.GetName()
}

func (r RenderableObject) Namespace() string {
	return r.GetNamespace()
}

// StatusConditions returns status.conditions sorted by type, ascending. Controllers don't
// guarantee a stable order for this list (see #787, where a Deployment's Available/Progressing
// conditions flipped order between runs), so every caller gets a deterministic order here rather
// than each render site having to tolerate either ordering on its own.
func (r RenderableObject) StatusConditions() (conditions []interface{}) {
	if x := r.Status()["conditions"]; x != nil {
		conditions = sortMapListByKeysValue("type", x.([]interface{}))
	}
	return
}

func (r RenderableObject) render(wr io.Writer) error {
	klog.V(5).InfoS("called render, calling findTemplateName", "r", r)
	tree, templateName := r.engine.templateSet.findTemplateName(r.Kind(), r.GroupVersionKind().Group)
	r.currentTree = tree
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

// HealthSummary renders this object's compact one-line summary: its own paired
// "<Kind>.summary"/"<Kind>.<group>.summary" template if one is defined -- built-in (living
// alongside that Kind's own <Kind>.tmpl) or a user override/CRD-author-provided one, discovered
// the same way a "<Kind>.tmpl" full view already is -- falling back to generic_health_summary
// otherwise. This is what resource_health_summary calls; see
// https://github.com/bergerx/kubectl-status/issues/826 for why it's a lookup rather than a
// hand-maintained per-Kind chain.
func (r RenderableObject) HealthSummary(callerNamespace string) (string, error) {
	data := map[string]interface{}{"obj": r, "callerNamespace": callerNamespace}
	if name, ok := r.engine.templateSet.findSummaryTemplateName(r.Kind(), r.GroupVersionKind().Group); ok {
		return r.renderTemplate(name, data)
	}
	return r.renderTemplate("generic_health_summary", data)
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
	if ok && target.Kind() == name && r.engine.renderedUIDs.checkAdd(target.GetUID()) && !r.Config.GetBool("watching") {
		klog.V(3).InfoS("skip rendering of the RenderableObject as its already rendered",
			"r", r, "templateName", name)
		_, _ = color.New(color.FgWhite).Fprintf(wr, "%s is already printed", target.String())
		return nil
	}
	tree := r.engine.templateSet.resolveIncludeTree(name, r.currentTree)
	return tree.ExecuteTemplate(wr, name, data)
}

type uidSet map[types.UID]struct{}

func (s uidSet) checkAdd(uid types.UID) bool {
	_, exists := s[uid]
	if !exists {
		s[uid] = struct{}{}
	}
	return exists
}
