package plugin

import "github.com/spf13/pflag"

// RenderOptions holds the options specific to kubectl-status.
type RenderOptions struct {
	IncludeOwners             bool
	IncludeEvents             bool
	IncludeMatchingServices   bool
	IncludeMatchingIngresses  bool
	IncludeApplicationDetails bool
	IncludeRolloutDiffs       bool
	Shallow                   bool
	Local                     bool
	Watch                     bool
}

// IncludesEnabled is for templates to use.
func (r RenderOptions) IncludesEnabled() bool {
	return !r.Shallow && !r.Local
}

func (r *RenderOptions) AddFlags(flags *pflag.FlagSet) {
	flags.BoolVar(&r.Local, "local", false,
		"Run the template against the provided yaml manifest. Need to be used with a --filename parameter. No request to apiserver is done.")
	flags.BoolVar(&r.IncludeOwners, "include-owners", true,
		"Follow the ownerReferences in the objects and render them as well.")
	flags.BoolVar(&r.IncludeEvents, "include-events", true,
		"Include events in the output.")
	flags.BoolVar(&r.IncludeMatchingServices, "include-matching-services", true,
		"Include Services matching the Pod in the output.")
	flags.BoolVar(&r.IncludeMatchingIngresses, "include-matching-ingresses", true,
		"Include Ingresses referencing the Service in the output.")
	flags.BoolVar(&r.IncludeApplicationDetails, "include-application-details", true,
		"This will include well known application metadata into the output.")
	flags.BoolVar(&r.IncludeRolloutDiffs, "include-rollout-diffs", false,
		"Include unified diff between stored revisions of Deployment, DaemonSet and StatefulSets.")
	flags.BoolVar(&r.Shallow, "shallow", false,
		"Render only the immediate object and disable all other --include-* flags. This will override any other flags.")
	flags.BoolVarP(&r.Watch, "watch", "w", false,
		"After listing/getting the requested object, watch for changes.")

}
