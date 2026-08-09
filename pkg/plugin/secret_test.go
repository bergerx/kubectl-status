package plugin

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// dataSecret builds a RenderableObject Secret of the given type with the given data map,
// base64-encoding each raw value the way `data:` is represented on the wire.
func dataSecret(secretType string, data map[string]string) RenderableObject {
	encoded := map[string]interface{}{}
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	obj := map[string]interface{}{
		"type": secretType,
		"data": encoded,
	}
	return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
}

func TestParseBasicAuthSecret(t *testing.T) {
	tests := []struct {
		name              string
		secret            RenderableObject
		wantUsername      bool
		wantUsernameEmpty bool
		wantPassword      bool
		wantPasswordEmpty bool
	}{
		{
			name:         "secret not found",
			secret:       RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}},
			wantUsername: false,
			wantPassword: false,
		},
		{
			name:         "both present",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "alice", "password": "hunter2"}),
			wantUsername: true,
			wantPassword: true,
		},
		{
			name:         "username only",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "alice"}),
			wantUsername: true,
			wantPassword: false,
		},
		{
			name:         "password only",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{"password": "hunter2"}),
			wantUsername: false,
			wantPassword: true,
		},
		{
			name:         "neither present",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{}),
			wantUsername: false,
			wantPassword: false,
		},
		{
			name:              "password key present but empty",
			secret:            dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "alice", "password": ""}),
			wantUsername:      true,
			wantPassword:      true,
			wantPasswordEmpty: true,
		},
		{
			name:              "username key present but empty",
			secret:            dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "", "password": "hunter2"}),
			wantUsername:      true,
			wantUsernameEmpty: true,
			wantPassword:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBasicAuthSecret(tt.secret)
			if got["HasUsername"] != tt.wantUsername {
				t.Errorf("HasUsername = %v, want %v", got["HasUsername"], tt.wantUsername)
			}
			if got["UsernameEmpty"] != tt.wantUsernameEmpty {
				t.Errorf("UsernameEmpty = %v, want %v", got["UsernameEmpty"], tt.wantUsernameEmpty)
			}
			if got["HasPassword"] != tt.wantPassword {
				t.Errorf("HasPassword = %v, want %v", got["HasPassword"], tt.wantPassword)
			}
			if got["PasswordEmpty"] != tt.wantPasswordEmpty {
				t.Errorf("PasswordEmpty = %v, want %v", got["PasswordEmpty"], tt.wantPasswordEmpty)
			}
		})
	}
}

func TestParseSSHAuthSecret(t *testing.T) {
	_, _, rsaKey := generateTestCert(t, genCertOptions{subjectCN: "irrelevant", selfSigned: true})
	validKeyPEM := keyPEMBytes(rsaKey)

	tests := []struct {
		name           string
		secret         RenderableObject
		wantExists     bool
		wantParseError bool
		wantKeyType    string
	}{
		{
			name:       "no ssh-privatekey entry",
			secret:     dataSecret("kubernetes.io/ssh-auth", map[string]string{}),
			wantExists: false,
		},
		{
			name:           "malformed base64",
			secret:         RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{"type": "kubernetes.io/ssh-auth", "data": map[string]interface{}{"ssh-privatekey": "not-valid-base64!!!"}}}},
			wantExists:     true,
			wantParseError: true,
		},
		{
			name:           "malformed pem",
			secret:         dataSecret("kubernetes.io/ssh-auth", map[string]string{"ssh-privatekey": "not a pem key"}),
			wantExists:     true,
			wantParseError: true,
		},
		{
			name:        "valid rsa private key",
			secret:      RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{"type": "kubernetes.io/ssh-auth", "data": map[string]interface{}{"ssh-privatekey": base64.StdEncoding.EncodeToString(validKeyPEM)}}}},
			wantExists:  true,
			wantKeyType: "ssh-rsa",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSSHAuthSecret(tt.secret)
			if got["Exists"] != tt.wantExists {
				t.Errorf("Exists = %v, want %v", got["Exists"], tt.wantExists)
			}
			if tt.wantParseError && got["ParseError"] == "" {
				t.Errorf("expected non-empty ParseError")
			}
			if !tt.wantParseError && got["ParseError"] != "" {
				t.Errorf("expected empty ParseError, got %q", got["ParseError"])
			}
			if tt.wantKeyType != "" && got["KeyType"] != tt.wantKeyType {
				t.Errorf("KeyType = %v, want %v", got["KeyType"], tt.wantKeyType)
			}
			if tt.wantKeyType != "" {
				fp, _ := got["Fingerprint"].(string)
				if !strings.HasPrefix(fp, "SHA256:") {
					t.Errorf("Fingerprint = %q, want SHA256: prefix", fp)
				}
			}
		})
	}
}

func TestParseServiceAccountTokenSecret(t *testing.T) {
	secretWith := func(annotations map[string]interface{}, data map[string]string) RenderableObject {
		encoded := map[string]interface{}{}
		for k, v := range data {
			encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		obj := map[string]interface{}{
			"type": "kubernetes.io/service-account-token",
			"data": encoded,
			"metadata": map[string]interface{}{
				"annotations": annotations,
			},
		}
		return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
	}

	t.Run("no service-account.name annotation", func(t *testing.T) {
		got := parseServiceAccountTokenSecret(secretWith(nil, nil))
		if got["HasServiceAccountName"] != false {
			t.Errorf("HasServiceAccountName = %v, want false", got["HasServiceAccountName"])
		}
		if got["HasToken"] != false {
			t.Errorf("HasToken = %v, want false", got["HasToken"])
		}
	})

	t.Run("annotation present but token not yet populated", func(t *testing.T) {
		got := parseServiceAccountTokenSecret(secretWith(map[string]interface{}{"kubernetes.io/service-account.name": "default"}, nil))
		if got["HasServiceAccountName"] != true {
			t.Errorf("HasServiceAccountName = %v, want true", got["HasServiceAccountName"])
		}
		if got["ServiceAccountName"] != "default" {
			t.Errorf("ServiceAccountName = %v, want default", got["ServiceAccountName"])
		}
		if got["HasToken"] != false {
			t.Errorf("HasToken = %v, want false", got["HasToken"])
		}
	})

	t.Run("populated by controller", func(t *testing.T) {
		got := parseServiceAccountTokenSecret(secretWith(map[string]interface{}{"kubernetes.io/service-account.name": "default"}, map[string]string{"token": "eyJ..."}))
		if got["HasServiceAccountName"] != true {
			t.Errorf("HasServiceAccountName = %v, want true", got["HasServiceAccountName"])
		}
		if got["HasToken"] != true {
			t.Errorf("HasToken = %v, want true", got["HasToken"])
		}
	})
}

func bootstrapTokenSecret(namespace, name string, data map[string]string) RenderableObject {
	encoded := map[string]interface{}{}
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	obj := map[string]interface{}{
		"type": "bootstrap.kubernetes.io/token",
		"data": encoded,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
	}
	return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
}

func TestParseBootstrapTokenSecret(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	ApplyTestHack(cfg)

	validData := map[string]string{
		"token-id":                       "abc123",
		"token-secret":                   "0123456789abcdef",
		"usage-bootstrap-authentication": "true",
		"usage-bootstrap-signing":        "true",
		"expiration":                     "2027-01-01T00:00:00Z",
	}

	t.Run("fully valid", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", validData))
		for key, want := range map[string]interface{}{
			"NamespaceOK":         true,
			"NameOK":              true,
			"TokenIDValid":        true,
			"TokenIDMatchesName":  true,
			"TokenSecretPresent":  true,
			"TokenSecretValid":    true,
			"HasExpiration":       true,
			"Expired":             false,
			"UsageAuthentication": true,
			"UsageSigning":        true,
		} {
			if got[key] != want {
				t.Errorf("%s = %v, want %v", key, got[key], want)
			}
		}
	})

	t.Run("wrong namespace", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("default", "bootstrap-token-abc123", validData))
		if got["NamespaceOK"] != false {
			t.Errorf("NamespaceOK = %v, want false", got["NamespaceOK"])
		}
	})

	t.Run("name doesn't match pattern", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "my-bootstrap-token", validData))
		if got["NameOK"] != false {
			t.Errorf("NameOK = %v, want false", got["NameOK"])
		}
	})

	t.Run("token-id doesn't match name suffix", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-zzzzzz", validData))
		if got["TokenIDValid"] != true {
			t.Errorf("TokenIDValid = %v, want true", got["TokenIDValid"])
		}
		if got["TokenIDMatchesName"] != false {
			t.Errorf("TokenIDMatchesName = %v, want false", got["TokenIDMatchesName"])
		}
	})

	t.Run("missing token-id and token-secret", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", map[string]string{}))
		want := []string{"token-id", "token-secret"}
		if !reflect.DeepEqual(got["MissingKeys"], want) {
			t.Errorf("MissingKeys = %v, want %v", got["MissingKeys"], want)
		}
	})

	t.Run("malformed token-id", func(t *testing.T) {
		data := map[string]string{"token-id": "BAD-ID", "token-secret": validData["token-secret"]}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["TokenIDValid"] != false {
			t.Errorf("TokenIDValid = %v, want false", got["TokenIDValid"])
		}
	})

	t.Run("malformed token-secret", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": "too-short"}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["TokenSecretValid"] != false {
			t.Errorf("TokenSecretValid = %v, want false", got["TokenSecretValid"])
		}
	})

	t.Run("expired", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": validData["token-secret"], "expiration": "2020-01-01T00:00:00Z"}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["HasExpiration"] != true {
			t.Errorf("HasExpiration = %v, want true", got["HasExpiration"])
		}
		if got["Expired"] != true {
			t.Errorf("Expired = %v, want true", got["Expired"])
		}
	})

	t.Run("no expiration set", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": validData["token-secret"]}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["HasExpiration"] != false {
			t.Errorf("HasExpiration = %v, want false", got["HasExpiration"])
		}
	})

	t.Run("usage flags absent default to disabled", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": validData["token-secret"]}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["UsageAuthentication"] != false {
			t.Errorf("UsageAuthentication = %v, want false", got["UsageAuthentication"])
		}
		if got["UsageSigning"] != false {
			t.Errorf("UsageSigning = %v, want false", got["UsageSigning"])
		}
	})
}

func TestSecretDataKeys(t *testing.T) {
	tests := []struct {
		name   string
		secret RenderableObject
		want   []string
	}{
		{
			name:   "secret not found",
			secret: RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}},
			want:   nil,
		},
		{
			name:   "data keys sorted",
			secret: dataSecret("Opaque", map[string]string{"z": "1", "a": "2"}),
			want:   []string{"a", "z"},
		},
		{
			name: "data and stringData deduplicated",
			secret: RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{
				"type": "Opaque",
				"data": map[string]interface{}{"a": base64.StdEncoding.EncodeToString([]byte("1"))},
				"stringData": map[string]interface{}{
					"a": "1",
					"b": "2",
				},
			}}},
			want: []string{"a", "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secretDataKeys(tt.secret)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("secretDataKeys() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
