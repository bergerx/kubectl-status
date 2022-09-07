package plugin

import (
	"context"
	"fmt"
	"os"
	_ "unsafe" // required for using go:linkname in the file

	"github.com/fatih/color"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/cli-runtime/pkg/resource"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/util/interrupt"
	kyaml "sigs.k8s.io/yaml"
)

//go:linkname signame runtime.signame
func signame(sig uint32) string

func Run(o *Options, args []string) error {
	engine, err := newRenderEngine(*o)
	if err != nil {
		klog.V(2).ErrorS(err, "Error creating engine")
		return err
	}
	klog.V(5).InfoS("Created engine", "engine", engine)
	if o.RenderOptions.Local {
		filenames := o.ResourceBuilderFlags.FileNameFlags.Filenames
		err := runLocal(filenames, engine)
		return err
	}
	return runRemote(args, engine)
}

func runRemote(args []string, engine *renderEngine) error {
	results, resourceInfos, err := engine.getQueriedResources(args)
	if err != nil {
		klog.V(1).ErrorS(err, "Error querying resources")
		return err
	}
	if !engine.RenderOptions.Watch && len(resourceInfos) == 0 {
		return fmt.Errorf("no resources found")
	}
	resourceCount := len(resourceInfos)
	klog.V(5).InfoS("Found matching resources", "count", resourceCount)
	for i, resourceInfo := range resourceInfos {
		item := fmt.Sprintf("%d/%d", i+1, resourceCount)
		klog.V(5).InfoS("Processing resource", "item", item, "resource", resourceInfo)
		obj := resourceInfo.Object
		processObj(obj, engine)
	}
	if engine.RenderOptions.Watch {
		return runWatch(results, engine)
	}
	return nil
}

func runWatch(results *resource.Result, engine *renderEngine) error {
	color.HiYellow("\nPrinted all existing resource statuses, starting to watch. Switching to shallow mode during watch!\n\n")
	engine.RenderOptions.Shallow = true
	klog.V(5).InfoS("Will run watch")
	obj, err := results.Object()
	if err != nil {
		klog.V(1).ErrorS(err, "Failed to get results object")
		return err
	}
	rv, err := meta.NewAccessor().ResourceVersion(obj)
	if err != nil {
		klog.V(1).ErrorS(err, "Watch failed to obtain resource version for list")
		return err
	}
	klog.V(5).InfoS("Starting watch with a specific resource version", "rv", rv)
	w, err := results.Watch(rv)
	if err != nil {
		klog.V(1).ErrorS(err, "Can't start watch")
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	intr := interrupt.New(nil, cancel)
	intr.Run(func() error {
		_, err := watchtools.UntilWithoutRetry(ctx, w, func(e watch.Event) (bool, error) {
			klog.V(5).InfoS("Processing watch event", "e", e)
			processObj(e.Object, engine)
			return false, nil
		})
		klog.V(1).ErrorS(err, "Watch failed", "obj", obj)
		return err
	})
	return nil
}

func processObj(obj runtime.Object, engine *renderEngine) {
	fmt.Printf("\n")
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		klog.V(0).ErrorS(err, "Failed to decode a resource", "obj", obj)
		return
	}
	r := newRenderableObject(out, *engine)
	err = r.render(os.Stdout)
	if err != nil {
		fmt.Printf("\n")
		klog.V(0).ErrorS(err, "Failed to render a resource", "obj", obj)
		return
	}
	fmt.Printf("\n")
}

func runLocal(filenames *[]string, engine *renderEngine) error {
	for _, filename := range *filenames {
		klog.V(5).InfoS("Processing local file", "filename", filename)
		out, err := kyamlUnmarshalFile(filename)
		if err != nil {
			klog.V(0).ErrorS(err, "Error unmarshalling the file", "filename", filename)
		}
		r := newRenderableObject(out, *engine)
		r.engine.RenderOptions.Shallow = true
		output, err := r.renderString()
		fmt.Println(output)
		if err != nil {
			klog.V(0).ErrorS(err, "Error processing file", "filename", filename)
		}
	}
	return nil
}

func kyamlUnmarshalFile(manifestFilename string) (out map[string]interface{}, err error) {
	manifestFile, _ := os.ReadFile(manifestFilename)
	err = kyaml.Unmarshal(manifestFile, &out)
	return
}
