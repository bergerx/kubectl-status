package plugin

import "testing"

func TestAPIGroup(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want string
	}{
		{name: "full apiVersion", in: "keyvault.azure.m.upbound.io/v1beta1", want: "keyvault.azure.m.upbound.io"},
		{name: "built-in group apiVersion", in: "apps/v1", want: "apps"},
		{name: "core apiVersion", in: "v1", want: ""},
		{name: "bare group", in: "cert-manager.io", want: "cert-manager.io"},
		{name: "bare version without a group", in: "v2beta1", want: ""},
		{name: "empty string", in: "", want: ""},
		{name: "nil", in: nil, want: ""},
		{name: "whitespace only", in: "   ", want: ""},
		{name: "empty group from a Flux inventory id", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiGroup(tt.in); got != tt.want {
				t.Errorf("apiGroup(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsBuiltinAPIGroup(t *testing.T) {
	builtin := []string{"", "apps", "batch", "policy", "autoscaling", "extensions",
		"networking.k8s.io", "rbac.authorization.k8s.io", "resource.k8s.io", "flowcontrol.apiserver.k8s.io"}
	for _, group := range builtin {
		if !isBuiltinAPIGroup(group) {
			t.Errorf("isBuiltinAPIGroup(%q) = false, want true", group)
		}
	}
	thirdParty := []string{"keyvault.azure.m.upbound.io", "cert-manager.io", "karpenter.sh",
		"monitoring.coreos.com", "kustomize.toolkit.fluxcd.io", "networking.istio.io", "appsx"}
	for _, group := range thirdParty {
		if isBuiltinAPIGroup(group) {
			t.Errorf("isBuiltinAPIGroup(%q) = true, want false", group)
		}
	}
}

func TestQualifyKind(t *testing.T) {
	tests := []struct {
		name              string
		kind              interface{}
		apiVersionOrGroup interface{}
		want              string
	}{
		{name: "third-party apiVersion", kind: "Secret", apiVersionOrGroup: "keyvault.azure.m.upbound.io/v1beta1", want: "Secret.keyvault.azure.m.upbound.io"},
		{name: "version is dropped so the preferred one is used", kind: "Gateway", apiVersionOrGroup: "gateway.networking.k8s.io/v1beta1", want: "Gateway.gateway.networking.k8s.io"},
		{name: "bare group", kind: "Gateway", apiVersionOrGroup: "gateway.networking.k8s.io", want: "Gateway.gateway.networking.k8s.io"},
		{name: "built-in groups are qualified too, the lookup wants them", kind: "Deployment", apiVersionOrGroup: "apps/v1", want: "Deployment.apps"},
		{name: "core group stays bare", kind: "Secret", apiVersionOrGroup: "v1", want: "Secret"},
		{name: "no group at all stays bare", kind: "Secret", apiVersionOrGroup: "", want: "Secret"},
		{name: "nil group stays bare", kind: "Secret", apiVersionOrGroup: nil, want: "Secret"},
		{name: "empty kind stays empty", kind: "", apiVersionOrGroup: "cert-manager.io", want: ""},
		{name: "nil kind stays empty", kind: nil, apiVersionOrGroup: "cert-manager.io", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qualifyKind(tt.kind, tt.apiVersionOrGroup); got != tt.want {
				t.Errorf("qualifyKind(%v, %v) = %q, want %q", tt.kind, tt.apiVersionOrGroup, got, tt.want)
			}
		})
	}
}

func TestDisplayKind(t *testing.T) {
	tests := []struct {
		name              string
		kind              interface{}
		apiVersionOrGroup interface{}
		want              string
	}{
		{name: "third-party group is shown", kind: "Secret", apiVersionOrGroup: "keyvault.azure.m.upbound.io/v1beta1", want: "Secret.keyvault.azure.m.upbound.io"},
		{name: "built-in group is hidden", kind: "Deployment", apiVersionOrGroup: "apps/v1", want: "Deployment"},
		{name: "k8s.io group is hidden", kind: "NetworkPolicy", apiVersionOrGroup: "networking.k8s.io/v1", want: "NetworkPolicy"},
		{name: "core group is hidden", kind: "Secret", apiVersionOrGroup: "v1", want: "Secret"},
		{name: "no group at all", kind: "Secret", apiVersionOrGroup: nil, want: "Secret"},
		{name: "empty kind stays empty", kind: nil, apiVersionOrGroup: "cert-manager.io", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayKind(tt.kind, tt.apiVersionOrGroup); got != tt.want {
				t.Errorf("displayKind(%v, %v) = %q, want %q", tt.kind, tt.apiVersionOrGroup, got, tt.want)
			}
		})
	}
}
