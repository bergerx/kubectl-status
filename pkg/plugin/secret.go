package plugin

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/yaml"
)

// parseDockerConfigSecret inspects a Secret expected to be type kubernetes.io/dockerconfigjson
// or the legacy kubernetes.io/dockercfg and extracts the configured registry hostnames only --
// never credentials, since those would end up in copy-pasted output. A missing/wrong registry
// entry is a common cause of ImagePullBackOff; Pod.tmpl's imagePullSecrets check only validates
// the Secret's type, not its contents.
func parseDockerConfigSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"Exists":     false,
		"WrongType":  false,
		"ActualType": "",
		"MissingKey": "",
		"ParseError": "",
		"Registries": []string{},
	}
	if secret.Object == nil {
		return result
	}
	result["Exists"] = true

	actualType, _ := secret.Object["type"].(string)
	result["ActualType"] = actualType

	var dataKey string
	switch actualType {
	case "kubernetes.io/dockerconfigjson":
		dataKey = ".dockerconfigjson"
	case "kubernetes.io/dockercfg":
		dataKey = ".dockercfg"
	default:
		result["WrongType"] = true
		return result
	}

	data, _ := secret.Object["data"].(map[string]interface{})
	encoded, ok := data[dataKey].(string)
	if !ok {
		result["MissingKey"] = dataKey
		return result
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode %s: %v", dataKey, err)
		return result
	}

	var auths map[string]interface{}
	if actualType == "kubernetes.io/dockerconfigjson" {
		var wrapper struct {
			Auths map[string]interface{} `json:"auths"`
		}
		if err := json.Unmarshal(decoded, &wrapper); err != nil {
			result["ParseError"] = fmt.Sprintf("failed to parse %s: %v", dataKey, err)
			return result
		}
		auths = wrapper.Auths
	} else if err := json.Unmarshal(decoded, &auths); err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse %s: %v", dataKey, err)
		return result
	}

	var registries []string
	for registry := range auths {
		registries = append(registries, registry)
	}
	sort.Strings(registries)
	if registries == nil {
		registries = []string{}
	}
	result["Registries"] = registries
	return result
}

// decodeSecretDataField base64-decodes data[key] as a Secret's data entry is encoded on the
// wire, returning ok=false if the key is absent or isn't a string. A key present but undecodable
// base64 is reported as present with an empty decoded value, since callers here only need to
// tell "absent" from "empty" from "has content", not surface a parse error for it.
func decodeSecretDataField(data map[string]interface{}, key string) (value string, ok bool) {
	encoded, ok := data[key].(string)
	if !ok {
		return "", false
	}
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	return string(decoded), true
}

// parseBasicAuthSecret inspects a Secret expected to be type kubernetes.io/basic-auth and
// reports only whether its username/password entries are present and non-empty -- never their
// values. An empty value (e.g. `--from-literal=password=`) is distinguished from a missing key:
// both are broken, but they point at different mistakes.
func parseBasicAuthSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"HasUsername":   false,
		"UsernameEmpty": false,
		"HasPassword":   false,
		"PasswordEmpty": false,
	}
	if secret.Object == nil {
		return result
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	if v, ok := decodeSecretDataField(data, "username"); ok {
		result["HasUsername"] = true
		result["UsernameEmpty"] = v == ""
	}
	if v, ok := decodeSecretDataField(data, "password"); ok {
		result["HasPassword"] = true
		result["PasswordEmpty"] = v == ""
	}
	return result
}

// parseSSHAuthSecret inspects a Secret expected to be type kubernetes.io/ssh-auth and reports
// whether its ssh-privatekey entry is present and parses as an SSH private key -- never the key
// material itself. KeyType/Fingerprint are derived from the *public* half of the key pair (the
// SHA256 fingerprint is the same one `ssh-keygen -lf` prints), mirroring the level of detail
// parseTLSSecretCertificate reports for kubernetes.io/tls (algorithm plus an identifying value),
// just without the certificate-specific fields (subject/issuer/validity) an SSH key doesn't have.
func parseSSHAuthSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"Exists":      false,
		"ParseError":  "",
		"KeyType":     "",
		"Fingerprint": "",
	}
	if secret.Object == nil {
		return result
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	encoded, ok := data["ssh-privatekey"].(string)
	if !ok {
		return result
	}
	result["Exists"] = true
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode ssh-privatekey: %v", err)
		return result
	}
	signer, err := ssh.ParsePrivateKey(decoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse ssh-privatekey: %v", err)
		return result
	}
	result["KeyType"] = signer.PublicKey().Type()
	result["Fingerprint"] = ssh.FingerprintSHA256(signer.PublicKey())
	return result
}

// parseServiceAccountTokenSecret inspects a Secret expected to be type
// kubernetes.io/service-account-token. Its data.token entry is populated asynchronously by the
// legacy TokenController once the kubernetes.io/service-account.name annotation names an
// existing ServiceAccount, so a freshly created Secret can legitimately carry the annotation but
// no token yet -- the template reports that as "waiting on controller", not as an error.
func parseServiceAccountTokenSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"HasServiceAccountName": false,
		"ServiceAccountName":    "",
		"HasToken":              false,
	}
	if secret.Object == nil {
		return result
	}
	if name, ok := secret.Annotations()["kubernetes.io/service-account.name"].(string); ok && name != "" {
		result["HasServiceAccountName"] = true
		result["ServiceAccountName"] = name
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	if token, ok := data["token"].(string); ok && token != "" {
		result["HasToken"] = true
	}
	return result
}

var (
	bootstrapTokenNameRegexp   = regexp.MustCompile(`^bootstrap-token-([a-z0-9]{6})$`)
	bootstrapTokenIDRegexp     = regexp.MustCompile(`^[a-z0-9]{6}$`)
	bootstrapTokenSecretRegexp = regexp.MustCompile(`^[a-z0-9]{16}$`)
)

// parseBootstrapTokenSecret inspects a Secret expected to be type bootstrap.kubernetes.io/token
// against the structural rules the bootstrap-token authenticator enforces at read time (see
// https://kubernetes.io/docs/reference/access-authn-authz/bootstrap-tokens/): it must live in
// kube-system, be named bootstrap-token-<token-id>, and carry a token-id/token-secret pair
// matching that id -- a Secret violating any of these is silently never honoured as a valid
// token, with no Kubernetes-side error to surface. token-secret's value is never reported, only
// whether it's present and well-formed. Expired is computed against cfg.Now() rather than
// time.Now() so it stays pinned under --test-hack.
func (cfg *RenderConfig) parseBootstrapTokenSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"NamespaceOK":         false,
		"ActualNamespace":     "",
		"NameOK":              false,
		"MissingKeys":         []string{},
		"TokenID":             "",
		"TokenIDValid":        false,
		"TokenIDMatchesName":  false,
		"TokenSecretPresent":  false,
		"TokenSecretValid":    false,
		"HasExpiration":       false,
		"Expiration":          time.Time{},
		"Expired":             false,
		"UsageAuthentication": false,
		"UsageSigning":        false,
	}
	if secret.Object == nil {
		return result
	}

	namespace := secret.Namespace()
	result["ActualNamespace"] = namespace
	result["NamespaceOK"] = namespace == "kube-system"

	nameMatch := bootstrapTokenNameRegexp.FindStringSubmatch(secret.Name())
	result["NameOK"] = nameMatch != nil

	data, _ := secret.Object["data"].(map[string]interface{})
	decode := func(key string) (string, bool) {
		encoded, ok := data[key].(string)
		if !ok {
			return "", false
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
		return string(decoded), true
	}

	var missingKeys []string
	tokenID, ok := decode("token-id")
	if !ok {
		missingKeys = append(missingKeys, "token-id")
	} else {
		result["TokenID"] = tokenID
		result["TokenIDValid"] = bootstrapTokenIDRegexp.MatchString(tokenID)
		if nameMatch != nil {
			result["TokenIDMatchesName"] = tokenID == nameMatch[1]
		}
	}
	tokenSecret, ok := decode("token-secret")
	if !ok {
		missingKeys = append(missingKeys, "token-secret")
	} else {
		result["TokenSecretPresent"] = true
		result["TokenSecretValid"] = bootstrapTokenSecretRegexp.MatchString(tokenSecret)
	}
	if missingKeys != nil {
		result["MissingKeys"] = missingKeys
	}

	if expiration, ok := decode("expiration"); ok && expiration != "" {
		if t, err := time.Parse(time.RFC3339, expiration); err == nil {
			result["HasExpiration"] = true
			result["Expiration"] = t
			result["Expired"] = t.Before(cfg.Now())
		}
	}

	if v, ok := decode("usage-bootstrap-authentication"); ok {
		result["UsageAuthentication"] = v == "true"
	}
	if v, ok := decode("usage-bootstrap-signing"); ok {
		result["UsageSigning"] = v == "true"
	}

	return result
}

// helmReleaseDataStruct is the subset of Helm's release.v1 JSON payload this template needs --
// intentionally not the full helm.sh/helm/v3/pkg/release.Release struct, to avoid pulling in the
// Helm SDK as a dependency just to read a handful of fields.
type helmReleaseDataStruct struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Manifest  string `json:"manifest"`
	Info      struct {
		Status      string `json:"status"`
		Description string `json:"description"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			AppVersion string `json:"appVersion"`
		} `json:"metadata"`
	} `json:"chart"`
}

// parseHelmReleaseSecret decodes a Secret of type helm.sh/release.v1 -- Helm (v3+)'s default
// release storage backend, one Secret per (release name, revision) -- into the fields the
// template needs. data.release is encoded twice: once as an ordinary Secret data entry (plain
// base64, per the Kubernetes API convention every Secret data value uses), then again by Helm's
// own storage driver as base64(gzip(JSON)); see decodeRelease/encodeRelease in
// https://github.com/helm/helm/blob/main/pkg/storage/driver/util.go.
func parseHelmReleaseSecret(secret RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"Exists":       false,
		"ParseError":   "",
		"ReleaseName":  "",
		"Namespace":    "",
		"Revision":     0,
		"Status":       "",
		"Description":  "",
		"ChartName":    "",
		"ChartVersion": "",
		"AppVersion":   "",
		"Manifest":     "",
	}
	if secret.Object == nil {
		return result
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	encoded, ok := data["release"].(string)
	if !ok {
		result["ParseError"] = "missing data.release"
		return result
	}
	result["Exists"] = true
	outer, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode release: %v", err)
		return result
	}
	inner, err := base64.StdEncoding.DecodeString(string(outer))
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode release payload: %v", err)
		return result
	}
	gzReader, err := gzip.NewReader(bytes.NewReader(inner))
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to gzip-decode release payload: %v", err)
		return result
	}
	defer gzReader.Close()
	plain, err := io.ReadAll(gzReader)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to decompress release payload: %v", err)
		return result
	}
	var release helmReleaseDataStruct
	if err := json.Unmarshal(plain, &release); err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse release JSON: %v", err)
		return result
	}
	result["ReleaseName"] = release.Name
	result["Namespace"] = release.Namespace
	result["Revision"] = release.Version
	result["Status"] = release.Info.Status
	result["Description"] = release.Info.Description
	result["ChartName"] = release.Chart.Metadata.Name
	result["ChartVersion"] = release.Chart.Metadata.Version
	result["AppVersion"] = release.Chart.Metadata.AppVersion
	result["Manifest"] = release.Manifest
	return result
}

// helmManifestDocSeparator matches Helm's own document separator convention: each rendered
// template in release.manifest is joined with a "---" line of its own (usually followed by a
// "# Source: <chart>/templates/..." comment), the same convention plain multi-document YAML uses.
var helmManifestDocSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// helmReleaseManifestResources splits a Helm release's rendered manifest into its individual
// Kubernetes objects and extracts just enough (apiVersion/kind/name/namespace) to look each one
// up live -- never re-rendering the full object spec here, since $.KubeGetFirst below fetches the
// object's actual current state rather than trusting what Helm last applied. Malformed or
// non-object documents (stray comments, empty docs from a trailing separator) are skipped rather
// than failing the whole list.
func helmReleaseManifestResources(manifest string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, doc := range helmManifestDocSeparator.Split(manifest, -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var parsed struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
			continue
		}
		if parsed.Kind == "" || parsed.Metadata.Name == "" {
			continue
		}
		out = append(out, map[string]interface{}{
			"apiVersion": parsed.APIVersion,
			"kind":       parsed.Kind,
			"name":       parsed.Metadata.Name,
			"namespace":  parsed.Metadata.Namespace,
		})
	}
	return out
}

// secretDataKeys returns the sorted union of a Secret's data and stringData key names, for
// Opaque secrets where there's no dedicated type-specific section to render -- listing the keys
// present gives a compact hint at the Secret's shape without exposing any values.
func secretDataKeys(secret RenderableObject) []string {
	var keys []string
	if secret.Object == nil {
		return keys
	}
	seen := map[string]bool{}
	for _, field := range []string{"data", "stringData"} {
		m, _ := secret.Object[field].(map[string]interface{})
		for key := range m {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys
}
