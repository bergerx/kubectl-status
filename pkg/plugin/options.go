package plugin

import (
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type Options struct {
	*genericclioptions.ConfigFlags
	*genericclioptions.ResourceBuilderFlags
	RenderOptions *RenderOptions
}

func NewOptions() *Options {
	return &Options{
		genericclioptions.NewConfigFlags(false),
		genericclioptions.NewResourceBuilderFlags().
			WithAll(false).
			WithAllNamespaces(false).
			WithFile(false).
			WithLabelSelector("").
			WithFieldSelector("").
			WithLatest(),
		&RenderOptions{},
	}
}

func (o Options) AddFlags(flags *pflag.FlagSet) {
	o.ConfigFlags.AddFlags(flags)
	o.ResourceBuilderFlags.AddFlags(flags)
}
