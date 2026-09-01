package plugin

import (
	"regexp"
	"strings"

	"github.com/spf13/cast"
)

// versionOnlyPattern matches an apiVersion that carries no group at all -- the core group's "v1"
// and the shape any group-less version takes ("v1beta1", "v2alpha1"). A reference field is
// variously populated with a full "group/version" (ownerReferences, Crossplane resourceRefs), a
// bare group (Gateway API backendRefs, cert-manager issuerRef, Flux inventory ids) or nothing, so
// apiGroup has to tell "v1" (core) apart from "cert-manager.io" (a group) without a cluster.
var versionOnlyPattern = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)

// apiGroup extracts the API group from a value that may be a full apiVersion ("apps/v1"), a bare
// group ("keyvault.azure.m.upbound.io"), a bare version ("v1"), or nothing. Returns "" for the core
// group and for anything it can't make sense of, so every caller degrades to the group-less
// behaviour that predates this rather than inventing a group.
//
// interface{} rather than string for the same reason resourceRef's parameters are: a ref field a
// CRD marks required can still arrive nil from a hand-written manifest rendered with -f, and
// cast.ToString degrades that to "" instead of erroring the call and aborting the object's render.
func apiGroup(apiVersionOrGroup interface{}) string {
	s := strings.TrimSpace(cast.ToString(apiVersionOrGroup))
	if s == "" {
		return ""
	}
	if group, _, found := strings.Cut(s, "/"); found {
		return group
	}
	if versionOnlyPattern.MatchString(s) {
		return ""
	}
	return s
}

// builtinAPIGroups are the non-core API groups the Kubernetes project itself serves that don't end
// in "k8s.io". Every other built-in group does (networking.k8s.io, rbac.authorization.k8s.io,
// storage.k8s.io, ...), which isBuiltinAPIGroup matches by suffix rather than by enumerating them.
var builtinAPIGroups = map[string]bool{
	"":            true,
	"apps":        true,
	"batch":       true,
	"policy":      true,
	"autoscaling": true,
	"extensions":  true,
}

// isBuiltinAPIGroup reports whether group is one Kubernetes itself serves. Kinds in those groups
// are the ones every operator already reads unqualified -- nobody needs to be told a Deployment is
// "Deployment.apps" -- so their group is left off the screen, while a third-party group is always
// shown: Kind alone is not a unique name, and a "Secret" from keyvault.azure.m.upbound.io rendered
// as plain "Secret/foo" reads as the core Secret it isn't (issue behind this helper).
//
// A CRD group that merely looks Kubernetes-ish still ends up qualified unless it is exactly one of
// the built-ins, since the check is on the whole group string and third-party groups don't end in
// "k8s.io" except by deliberate squatting (sigs.k8s.io/gateway-api's gateway.networking.k8s.io
// being the notable, and harmless, exception -- its Kinds don't collide with built-in ones).
func isBuiltinAPIGroup(group string) bool {
	return builtinAPIGroups[group] || group == "k8s.io" || strings.HasSuffix(group, ".k8s.io")
}

// qualifyKind builds the group-qualified TYPE argument for KubeGet/KubeGetFirst out of a
// reference's kind plus whatever it says about its API group -- a full apiVersion
// ("keyvault.azure.m.upbound.io/v1beta1"), a bare group ("gateway.networking.k8s.io"), a bare
// version ("v1"), or nothing. Returns kind unchanged for the core group, "Kind.group" otherwise.
//
// Templates resolving a reference whose API group they know should always qualify it, since a bare
// kind lets the RESTMapper resolve across every group sharing that Kind name and silently pick
// whichever wins the tie -- Gateway API's Gateway vs. Istio's (issue #789), or a Crossplane XR
// composing a "Secret" from keyvault.azure.m.upbound.io, whose lookup landed on the core Secret of
// the same name and rendered an unrelated object's contents.
//
// The version is deliberately dropped even when the reference carries one (resource.Builder would
// honour it, see input.mappingFor), so the lookup follows the CRD's currently-preferred version
// instead of failing once the version recorded in an old reference stops being served.
//
// The parameters are interface{} rather than string for the same reason resourceRef's are: a ref
// field a CRD marks required can still arrive nil from a hand-written manifest rendered with -f,
// and cast.ToString degrades that to "" instead of erroring the call and aborting the render.
func qualifyKind(kind, apiVersionOrGroup interface{}) string {
	kindStr := cast.ToString(kind)
	group := apiGroup(apiVersionOrGroup)
	if kindStr == "" || group == "" {
		return kindStr
	}
	return kindStr + "." + group
}

// displayKind renders a Kind for the screen, qualified as "Kind.group" only when the group is one
// Kubernetes doesn't serve itself. This is the display counterpart of qualifyKind: the lookup
// form always carries the group (the apiserver needs it to resolve the right resource), while the
// printed form carries it only when it tells the reader something -- "Deployment.apps" is noise,
// "Secret.keyvault.azure.m.upbound.io" is the whole point. Used by resourceRef for every reference
// on screen, and directly by templates that print a bare kind (e.g. a Flux Kustomization's
// per-kind inventory counts).
func displayKind(kind, apiVersionOrGroup interface{}) string {
	kindStr := cast.ToString(kind)
	group := apiGroup(apiVersionOrGroup)
	if kindStr == "" || group == "" || isBuiltinAPIGroup(group) {
		return kindStr
	}
	return kindStr + "." + group
}
