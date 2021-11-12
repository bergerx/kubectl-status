package plugin

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
}

// IncludesEnabled is for templates to use.
func (r RenderOptions) IncludesEnabled() bool {
	return !r.Shallow && !r.Local
}
