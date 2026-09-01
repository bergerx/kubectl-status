package plugin

import (
	"strings"

	"github.com/spf13/cast"
)

// expectedHostnames normalises a hostnames argument that templates pass either as a single
// hostname or as a list, dropping the empty entries an unset Gateway listener or route hostname
// produces.
func expectedHostnames(hostnames interface{}) []string {
	var raw []string
	switch v := hostnames.(type) {
	case nil:
	case string:
		// Not cast.ToStringSlice: that splits a plain string on whitespace.
		raw = []string{v}
	default:
		raw = cast.ToStringSlice(v)
	}
	var out []string
	for _, hostname := range raw {
		if hostname != "" {
			out = append(out, hostname)
		}
	}
	return out
}

// hostnameIntersections narrows a Gateway listener's hostname against a route's hostnames the
// way the Gateway API attaches routes to listeners, returning the hostnames the pair actually
// serves together -- which is what a listener's certificate has to cover.
//
// Templates previously compared the two sides as exact strings, which is wrong on both counts a
// Gateway API hostname can be a wildcard: a "*.example.com" listener does serve a
// "foo.example.com" route (so the listener's certificate should be checked, not skipped), and
// the pair's effective hostname is the narrower of the two, not the wildcard. Wildcards here are
// suffix matches of any depth, per the Gateway API hostname spec -- unlike a certificate
// wildcard, which spans exactly one label.
//
// An empty listener hostname means the listener serves every hostname, so the route's own
// hostnames stand unnarrowed. Empty route hostnames mean the route accepts whatever the listener
// serves, which the listener's own certificate check already covers, so nothing is returned. An
// empty result for two non-empty sides means they are disjoint: the route does not attach here.
func hostnameIntersections(listenerHostname string, routeHostnames interface{}) []string {
	hostnames := expectedHostnames(routeHostnames)
	if len(hostnames) == 0 {
		return nil
	}
	if listenerHostname == "" {
		return hostnames
	}
	var intersections []string
	for _, routeHostname := range hostnames {
		if narrowed := narrowerHostname(listenerHostname, routeHostname); narrowed != "" {
			intersections = append(intersections, narrowed)
		}
	}
	return intersections
}

// narrowerHostname returns the more specific of two Gateway API hostname patterns, or "" when
// they share no hostname at all.
func narrowerHostname(a, b string) string {
	aSuffix, aIsWildcard := wildcardSuffix(a)
	bSuffix, bIsWildcard := wildcardSuffix(b)
	switch {
	case !aIsWildcard && !bIsWildcard:
		if strings.EqualFold(a, b) {
			return a
		}
	case aIsWildcard && !bIsWildcard:
		// A wildcard covers subdomains of its suffix at any depth, but never the suffix itself.
		if hostnameUnder(b, aSuffix) {
			return b
		}
	case !aIsWildcard && bIsWildcard:
		if hostnameUnder(a, bSuffix) {
			return a
		}
	default:
		// Two wildcards overlap when one suffix contains the other; the deeper one wins.
		if strings.EqualFold(aSuffix, bSuffix) || hostnameUnder(aSuffix, bSuffix) {
			return a
		}
		if hostnameUnder(bSuffix, aSuffix) {
			return b
		}
	}
	return ""
}

// hostnameUnder reports whether host is a strict subdomain of suffix.
func hostnameUnder(host, suffix string) bool {
	return strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(suffix))
}

// IstioHostRef is the resolved form of an Istio host reference -- the string a VirtualService
// destination, a VirtualService/DestinationRule host or a mirror target points at.
type IstioHostRef struct {
	// Key is the canonical form two host references compare on. Two references with the same
	// Key name the same thing; an empty Key means there was nothing to resolve.
	Key string
	// Name and Namespace are the Service the reference resolves to. Both empty unless InCluster.
	Name      string
	Namespace string
	// InCluster is true when the reference names a Service in this cluster, as opposed to an
	// external host (usually backed by a ServiceEntry) or a wildcard pattern.
	InCluster bool
}

// istioHost resolves an Istio host reference against the namespace of the object naming it, so
// that two spellings of the same service compare equal.
//
// Istio accepts a short name, a namespaced name, and both service-domain forms for one and the
// same Service -- "reviews", "reviews.default", "reviews.default.svc" and
// "reviews.default.svc.cluster.local" are interchangeable -- and resolves the short form
// against the namespace of the object doing the naming, not the destination's. So a
// VirtualService destination and the DestinationRule meant to pair with it routinely disagree
// on spelling while meaning the same Service, and a string comparison of the two would miss
// most real pairs.
//
// Anything that isn't an in-cluster Service -- an external host a ServiceEntry backs
// ("api.example.com"), a wildcard ("*.example.com") -- keeps its own spelling as the Key and is
// left InCluster=false, so those only ever pair with an identical spelling and never get looked
// up as a Service. That is deliberately conservative: callers use the result to decide whether
// two objects are talking about the same thing before reporting a problem with the pair, and a
// wrong pairing would invent a problem that isn't there.
func istioHost(host, namespace string) IstioHostRef {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" || strings.Contains(h, "*") {
		return IstioHostRef{Key: h}
	}
	parts := strings.Split(h, ".")
	name := parts[0]
	// A bare name is the destination's own namespace; "name.namespace" and the ".svc"/
	// ".svc.cluster.local" forms carry it in the second label. Everything else -- an external
	// DNS name like "api.example.com" -- has no namespace to speak of.
	switch {
	case len(parts) == 1:
		if namespace == "" {
			break
		}
		return IstioHostRef{Key: name + "." + namespace, Name: name, Namespace: namespace, InCluster: true}
	case len(parts) == 2, parts[2] == "svc":
		return IstioHostRef{Key: name + "." + parts[1], Name: name, Namespace: parts[1], InCluster: true}
	}
	return IstioHostRef{Key: h}
}
