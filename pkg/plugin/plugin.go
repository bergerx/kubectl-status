package plugin

import (
	"context"
	"fmt"
	"io"
	_ "unsafe" // required for using go:linkname in the file

	"github.com/fatih/color"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/util/interrupt"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func errorPrintf(wr io.Writer, format string, a ...interface{}) {
	_, _ = color.New(color.BgRed, color.FgHiWhite).Printf(format, a...)
	_, _ = fmt.Fprintln(wr)
}

func Run(f util.Factory, streams genericiooptions.IOStreams, args []string, cfg *RenderConfig) error {
	klog.V(5).InfoS("All config settings", "settings", cfg.Viper.AllSettings())
	if cfg.Viper.Get("color") == "always" {
		color.NoColor = false
	} else if cfg.Viper.Get("color") == "never" {
		color.NoColor = true
	}
	repo, err := input.NewResourceRepo(f, cfg.Viper)
	if err != nil {
		klog.V(2).ErrorS(err, "Error creating repo")
		return err
	}
	engine, err := newRenderEngine(streams, cfg)
	if err != nil {
		klog.V(2).ErrorS(err, "Error creating engine")
		return err
	}
	klog.V(5).InfoS("Created engine", "engine", engine)
	results := repo.CLIQueryResults(args)
	count := 0
	err = results.Visit(func(resourceInfo *resource.Info, err error) error {
		count += 1
		klog.V(5).InfoS("Processing resource", "item", count, "resource", resourceInfo)
		processObj(resourceInfo.Object, engine, repo)
		return err
	})
	klog.V(5).InfoS("Processed matching resources", "count", count)
	if err != nil {
		klog.V(1).ErrorS(err, "Error querying resources")
		return err
	}
	isWatch := cfg.Viper.GetBool("watch")
	if !isWatch && count == 0 {
		return fmt.Errorf("no resources found")
	}
	if cfg.Viper.GetBool("watch") {
		return runWatch(results, engine, repo, cfg)
	}
	return nil
}

func runWatch(results *resource.Result, engine *renderEngine, repo *input.ResourceRepo, cfg *RenderConfig) error {
	color.HiYellow("\nPrinted all existing resource statuses, starting to watch. Switching to shallow mode during watch!\n\n")
	cfg.Viper.Set("shallow", true)
	cfg.Viper.Set("watching", true)
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
	_ = intr.Run(func() error {
		_, err := watchtools.UntilWithoutRetry(ctx, w, func(e watch.Event) (bool, error) {
			klog.V(5).InfoS("Processing watch event", "e", e)
			processObj(e.Object, engine, repo)
			return false, nil
		})
		klog.V(1).ErrorS(err, "Watch failed", "obj", obj)
		return err
	})
	return nil
}

func processObj(obj runtime.Object, engine *renderEngine, repo *input.ResourceRepo) {
	streams := engine.ioStreams
	_, _ = fmt.Fprintf(streams.Out, "\n")
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		errorPrintf(streams.ErrOut, "Failed to decode obj=%s: %s", obj, err)
		return
	}
	engine.renderedUIDs = make(uidSet)
	r := newRenderableObject(out, engine, repo)
	err = r.render(streams.Out)
	if err != nil {
		_, _ = fmt.Fprintf(streams.ErrOut, "\n")
		errorPrintf(streams.ErrOut, "Failed to render: %s", err)
		return
	}
	_, _ = fmt.Fprintf(streams.Out, "\n")
}
