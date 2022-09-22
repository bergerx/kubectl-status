package plugin

import (
	"bufio"
	"context"
	"fmt"
	"github.com/spf13/viper"
	"io"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/kubectl/pkg/cmd/util"
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

func Run(f util.Factory, args []string) error {
	klog.V(5).InfoS("All config settings", "settings", viper.AllSettings())
	engine, err := newRenderEngine(f)
	if err != nil {
		klog.V(2).ErrorS(err, "Error creating engine")
		return err
	}
	klog.V(5).InfoS("Created engine", "engine", engine)
	if viper.GetBool("local") {
		err := runLocal(engine)
		return err
	}
	return runRemote(args, engine)
}

func runRemote(args []string, engine *renderEngine) error {
	results := engine.newBuilder().
		FilenameParam(false, &resource.FilenameOptions{
			Filenames: viper.GetStringSlice("filename"),
			Recursive: viper.GetBool("recursive"),
		}).
		LabelSelectorParam(viper.GetString("selector")).
		FieldSelectorParam(viper.GetString("field-selector")).
		ResourceTypeOrNameArgs(true, args...).
		ContinueOnError().
		Do()
	resourceInfos, err := results.Infos() // []*resource.Info
	if err != nil {
		klog.V(1).ErrorS(err, "Error querying resources")
		return err
	}
	isWatch := viper.GetBool("watch")
	if !isWatch && len(resourceInfos) == 0 {
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
	if viper.GetBool("watch") {
		return runWatch(results, engine)
	}
	return nil
}

func runWatch(results *resource.Result, engine *renderEngine) error {
	color.HiYellow("\nPrinted all existing resource statuses, starting to watch. Switching to shallow mode during watch!\n\n")
	viper.Set("shallow", true)
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

func runLocal(engine *renderEngine) error {
	for _, filename := range viper.GetStringSlice("filename") {
		klog.V(5).InfoS("Processing local file", "filename", filename)
		f, err := os.Open(filename)
		if err != nil {
			klog.V(0).ErrorS(err, "Failed to open file", "filename", filename)
			return err
		}
		yr := yaml.NewYAMLReader(bufio.NewReader(f))
		eof := false
		for !eof {
			b, err := yr.Read()
			if err == io.EOF {
				klog.V(10).InfoS("Reached end of the file", "filename", filename)
				eof = true
			} else if err != nil {
				klog.V(3).ErrorS(err, "Error reading file", "filename", filename)
				continue
			}
			if len(b) == 0 {
				continue
			}
			klog.V(10).InfoS("Parsing document in the file", "document", b)
			var out map[string]interface{}
			err = kyaml.Unmarshal(b, &out)
			if err != nil {
				klog.V(3).ErrorS(err, "Error parsing document in the file", "filename", filename)
				continue
			}
			if items, ok := out["items"]; ok {
				for _, obj := range items.([]interface{}) {
					renderObj(engine, obj.(map[string]interface{}))
				}
			} else {
				renderObj(engine, out)
			}
		}
	}
	return nil
}

func renderObj(engine *renderEngine, out map[string]interface{}) {
	r := newRenderableObject(out, *engine)
	output, err := r.renderString()
	fmt.Println(output)
	if err != nil {
		klog.V(0).ErrorS(err, "Error rendering resource")
	}
	fmt.Println("")
}
