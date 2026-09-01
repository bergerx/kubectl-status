package plugin

import (
	"reflect"
	"testing"
)

func TestHostnameIntersections(t *testing.T) {
	tests := []struct {
		name             string
		listenerHostname string
		routeHostnames   interface{}
		want             []string
	}{
		{
			name:             "route pins no hostname, so nothing narrows the listener",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{},
			want:             nil,
		},
		{
			name:             "listener serves every hostname, so the route's stand unnarrowed",
			listenerHostname: "",
			routeHostnames:   []string{"a.example.com", "*.b.example.com"},
			want:             []string{"a.example.com", "*.b.example.com"},
		},
		{
			name:             "identical concrete hostnames",
			listenerHostname: "a.example.com",
			routeHostnames:   []string{"a.example.com"},
			want:             []string{"a.example.com"},
		},
		{
			name:             "different concrete hostnames are disjoint",
			listenerHostname: "a.example.com",
			routeHostnames:   []string{"b.example.com"},
			want:             nil,
		},
		{
			// The listener attaches the route, and the pair only ever serves the route's
			// concrete hostname -- so that, not the wildcard, is what the cert must cover.
			name:             "wildcard listener narrows to the route's concrete hostname",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"a.example.com", "deep.a.example.com", "other.org"},
			want:             []string{"a.example.com", "deep.a.example.com"},
		},
		{
			name:             "concrete listener narrows a wildcard route to itself",
			listenerHostname: "a.example.com",
			routeHostnames:   []string{"*.example.com"},
			want:             []string{"a.example.com"},
		},
		{
			name:             "a wildcard never covers its own suffix",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"example.com"},
			want:             nil,
		},
		{
			name:             "two wildcards keep the deeper one",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"*.apps.example.com"},
			want:             []string{"*.apps.example.com"},
		},
		{
			name:             "two wildcards keep the deeper one, listener side",
			listenerHostname: "*.apps.example.com",
			routeHostnames:   []string{"*.example.com"},
			want:             []string{"*.apps.example.com"},
		},
		{
			name:             "identical wildcards",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"*.example.com"},
			want:             []string{"*.example.com"},
		},
		{
			name:             "sibling wildcards are disjoint",
			listenerHostname: "*.a.example.com",
			routeHostnames:   []string{"*.b.example.com"},
			want:             nil,
		},
		{
			name:             "matching is case-insensitive",
			listenerHostname: "*.EXAMPLE.com",
			routeHostnames:   []string{"A.example.COM"},
			want:             []string{"A.example.COM"},
		},
		{
			name:             "untyped list from unstructured data",
			listenerHostname: "*.example.com",
			routeHostnames:   []interface{}{"a.example.com"},
			want:             []string{"a.example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hostnameIntersections(tt.listenerHostname, tt.routeHostnames)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("hostnameIntersections(%q, %#v) = %#v, want %#v", tt.listenerHostname, tt.routeHostnames, got, tt.want)
			}
		})
	}
}

func TestIstioHost(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		namespace string
		want      IstioHostRef
	}{
		{
			name:      "short name resolves against the naming object's namespace",
			host:      "reviews",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.bookinfo", Name: "reviews", Namespace: "bookinfo", InCluster: true},
		},
		{
			name:      "namespaced name carries its own namespace",
			host:      "reviews.other",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "svc form",
			host:      "reviews.other.svc",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "fully qualified cluster-local form",
			host:      "reviews.other.svc.cluster.local",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			// The whole point: these four spellings have to pair with each other, which is what
			// a VirtualService destination and its DestinationRule routinely disagree on.
			name:      "trailing dot and casing don't change the key",
			host:      "Reviews.Other.SVC.Cluster.Local.",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "external host keeps its own spelling and resolves to no Service",
			host:      "api.example.com",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "api.example.com"},
		},
		{
			name:      "a custom cluster domain is still recognised by its svc label",
			host:      "reviews.other.svc.mesh.internal",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "wildcard never resolves to a Service",
			host:      "*.example.com",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "*.example.com"},
		},
		{
			name:      "bare wildcard",
			host:      "*",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "*"},
		},
		{
			name:      "empty host",
			host:      "",
			namespace: "bookinfo",
			want:      IstioHostRef{},
		},
		{
			// Cluster-scoped renders (and -f files with no namespace) have nothing to resolve a
			// short name against, so it stays unresolved rather than pairing with whatever
			// short name happens to match.
			name:      "short name without a namespace to resolve against",
			host:      "reviews",
			namespace: "",
			want:      IstioHostRef{Key: "reviews"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := istioHost(tt.host, tt.namespace)
			if got != tt.want {
				t.Errorf("istioHost(%q, %q) = %#v, want %#v", tt.host, tt.namespace, got, tt.want)
			}
		})
	}
}
