package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"

	"github.com/fatih/color"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/cli-runtime/pkg/resource"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/util/interrupt"
)

func Run(ctx context.Context, o *Options, args []string, output io.Writer) error {
	tmpl, err := getTemplate()
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing templates")
		return err
	}
	engine, err := newRenderEngine(*o, output, 0, tmpl)
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
	return runRemote(ctx, args, engine)
}

func runRemote(ctx context.Context, args []string, engine *renderEngine) error {
	results, resourceInfos, err := engine.getQueriedResources(args)
	if err != nil {
		klog.V(1).ErrorS(err, "Error querying resources")
		return err
	}
	resourceCount := len(resourceInfos)
	if !engine.RenderOptions.Watch && resourceCount == 0 {
		return fmt.Errorf("no resources found")
	}
	klog.V(5).InfoS("Found matching resources", "count", resourceCount)
	for i, resourceInfo := range resourceInfos {
		if errors.Is(ctx.Err(), context.Canceled) {
			os.Exit(2)
		}
		item := fmt.Sprintf("%d/%d", i+1, resourceCount)
		klog.V(5).InfoS("Processing resource", "item", item, "resource", resourceInfo)
		obj := resourceInfo.Object
		processObj(obj, engine)
	}
	if engine.RenderOptions.Watch {
		return runWatch(ctx, results, engine)
	}
	return nil
}

func runWatch(ctx context.Context, results *resource.Result, engine *renderEngine) error {
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
	ctx, cancel := context.WithCancel(ctx)
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
	fmt.Fprintf(engine.Output, "\n")
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		klog.V(0).ErrorS(err, "Failed to decode a resource", "obj", obj)
		return
	}
	r := newRenderableObject(out, engine)
	err = r.render()
	if err != nil {
		fmt.Fprintf(engine.Output, "\n")
		klog.V(0).ErrorS(err, "Failed to render a resource", "obj", obj)
		return
	}
	fmt.Fprintf(engine.Output, "\n")
}

func runLocal(filenames *[]string, engine *renderEngine) error {
	var errstrings []string
	for _, filename := range *filenames {
		klog.V(5).InfoS("Processing local file", "filename", filename)
		out, err := kyamlUnmarshalFile(filename)
		if err != nil {
			klog.V(0).ErrorS(err, "Error unmarshalling the file", "filename", filename)
			errstrings = append(errstrings, err.Error())
		}
		r := newRenderableObject(out, engine)
		r.engine.RenderOptions.Shallow = true
		err = r.render()
		if err != nil {
			klog.V(0).ErrorS(err, "Error processing file", "filename", filename)
			errstrings = append(errstrings, err.Error())
		}
	}
	if len(errstrings) > 0 {
		return errors.New(strings.Join(errstrings, "\n"))
	}
	return nil
}

func kyamlUnmarshalFile(manifestFilename string) (out map[string]interface{}, err error) {
	manifestFile, err := ioutil.ReadFile(manifestFilename)
	if err != nil {
		return
	}
	err = yaml.Unmarshal(manifestFile, &out)
	return
}
