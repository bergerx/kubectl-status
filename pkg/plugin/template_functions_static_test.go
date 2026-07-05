package plugin

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	emptyMap     = map[string]interface{}{}
	searchForMap = map[string]interface{}{
		"searchKey1": "searchVal1",
		"searchKey2": "searchVal2",
	}
	nonMatchingValueMap = map[string]interface{}{
		"searchKey": "searchValDoesntMatch",
	}
	nonMatchingKeyMap = map[string]interface{}{
		"searchKeyDoesntMatch": "searchVal",
	}
	matchingSuperSetMap1 = map[string]interface{}{
		"searchKey1": "searchVal1",
		"searchKey2": "searchVal2",
		"otherKey1":  "doestMatter1",
	}
	matchingSuperSetMap2 = map[string]interface{}{
		"searchKey1": "searchVal1",
		"searchKey2": "searchVal2",
		"otherKey2":  "doestMatter2",
	}
	nestedSearchForMap = map[string]interface{}{
		"outerKey.innerKey.searchKey1": "searchVal1",
		"outerKey.innerKey.searchKey2": "searchVal2",
	}
	matchingNestedMap = map[string]interface{}{
		"outerKey": map[string]interface{}{
			"innerKey": matchingSuperSetMap1,
			"otherKey": "doesntMatter",
		},
	}
	nonMatchingMiddleKeyNestedMap = map[string]interface{}{
		"outerKey": matchingSuperSetMap1,
	}
)

func TestGetMatchingItemInMapList(t *testing.T) {
	type args struct {
		searchFor map[string]interface{}
		mapList   []interface{}
	}
	tests := []struct {
		name     string
		args     args
		wantItem map[string]interface{}
	}{
		{
			name: "one-to-one maps",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{searchForMap},
			},
			wantItem: searchForMap,
		}, {
			name: "key exists but value doesn't match",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingValueMap},
			},
			wantItem: nil,
		}, {
			name: "search key doesnt exist in mapList",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingKeyMap},
			},
			wantItem: nil,
		}, {
			name: "empty mapList",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{emptyMap},
			},
			wantItem: nil,
		}, {
			name: "empty searchFor",
			args: args{
				searchFor: emptyMap,
				mapList:   []interface{}{searchForMap},
			},
			wantItem: nil,
		}, {
			name: "searchFor is subset",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingKeyMap, nonMatchingValueMap, matchingSuperSetMap1},
			},
			wantItem: matchingSuperSetMap1,
		}, {
			name: "multiple matches should return first match",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingKeyMap, nonMatchingValueMap, matchingSuperSetMap2, matchingSuperSetMap1},
			},
			wantItem: matchingSuperSetMap2,
		}, {
			name: "nested map is subset",
			args: args{
				searchFor: nestedSearchForMap,
				mapList:   []interface{}{nonMatchingKeyMap, nonMatchingValueMap, matchingSuperSetMap1, matchingNestedMap},
			},
			wantItem: matchingNestedMap,
		}, {
			name: "nested map missing key",
			args: args{
				searchFor: nestedSearchForMap,
				mapList:   []interface{}{nonMatchingKeyMap},
			},
			wantItem: nil,
		}, {
			name: "nested map missing middle key",
			args: args{
				searchFor: nestedSearchForMap,
				mapList:   []interface{}{nonMatchingMiddleKeyNestedMap},
			},
			wantItem: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotItem := getMatchingItemInMapList(tt.args.searchFor, tt.args.mapList); !reflect.DeepEqual(gotItem, tt.wantItem) {
				t.Errorf("getMatchingItemInMapList() = %v, want %v", gotItem, tt.wantItem)
			}
		})
	}
}

func TestSortMapListByKeysValueIsStableOnTies(t *testing.T) {
	// When multiple items share the same key value, the sort must preserve
	// their original relative order (as returned by the k8s API) instead of
	// reordering them arbitrarily, otherwise output like "Known/recorded
	// manage events" becomes flaky between otherwise identical runs.
	mapList := []interface{}{
		map[string]interface{}{"manager": "kubectl-client-side-apply", "time": "2024-01-01T00:00:00Z"},
		map[string]interface{}{"manager": "kube-controller-manager", "time": "2024-01-01T00:00:00Z"},
		map[string]interface{}{"manager": "another-manager", "time": "2023-12-31T00:00:00Z"},
	}
	for i := 0; i < 10; i++ {
		got := sortMapListByKeysValue("time", mapList)
		want := []interface{}{mapList[2], mapList[0], mapList[1]}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sortMapListByKeysValue() = %v, want %v", got, want)
		}
	}
}

func TestAgoSuffix(t *testing.T) {
	viper.Set("absolute-time", false)
	if got := agoSuffix(); got != " ago" {
		t.Errorf("agoSuffix() = %q, want %q", got, " ago")
	}
	viper.Set("absolute-time", true)
	if got := agoSuffix(); got != "" {
		t.Errorf("agoSuffix() = %q, want empty string", got)
	}
	viper.Set("absolute-time", false)
}

func TestForOrSince(t *testing.T) {
	viper.Set("absolute-time", false)
	if got := forOrSince(); got != "for" {
		t.Errorf("forOrSince() = %q, want %q", got, "for")
	}
	viper.Set("absolute-time", true)
	if got := forOrSince(); got != "since" {
		t.Errorf("forOrSince() = %q, want %q", got, "since")
	}
	viper.Set("absolute-time", false)
}

func TestColorAgoAbsolute(t *testing.T) {
	viper.Set("absolute-time", true)
	input := "2006-01-02T15:04:05Z"
	if got := colorAgo(input); got != input {
		t.Errorf("colorAgo(%q) = %q, want %q", input, got, input)
	}
	viper.Set("absolute-time", false)
}

// genCertOptions configures generateTestCert.
type genCertOptions struct {
	subjectCN  string
	dnsNames   []string
	isCA       bool
	selfSigned bool
	// parent/parentKey are used when selfSigned is false, to sign this cert with another.
	parent    *x509.Certificate
	parentKey *rsa.PrivateKey
}

// generateTestCert generates an in-memory RSA key + x509 certificate PEM block, either
// self-signed or signed by the given parent, avoiding any static/expiring PEM fixtures.
func generateTestCert(t *testing.T, opts genCertOptions) (certPEM []byte, cert *x509.Certificate, key *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: opts.subjectCN},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour * 365),
		DNSNames:     opts.dnsNames,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         opts.isCA,
	}
	if opts.isCA {
		template.BasicConstraintsValid = true
	}

	parent := template
	signerKey := key
	if !opts.selfSigned {
		parent = opts.parent
		signerKey = opts.parentKey
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	cert, err = x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("failed to parse generated certificate: %v", err)
	}
	return certPEM, cert, key
}

// keyPEM PEM-encodes an RSA private key in PKCS1 form (content is irrelevant to
// parseTLSSecretCertificate, which never inspects tls.key, only checks for its presence).
func keyPEMBytes(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// tlsSecret builds a RenderableObject wrapping a kubernetes.io/tls Secret with the given
// (already base64-encoded-ready, i.e. raw) tls.crt/tls.key byte contents. Passing nil for
// either omits that key from data entirely (simulating a missing key).
func tlsSecret(secretType string, crt, key []byte) RenderableObject {
	data := map[string]interface{}{}
	if crt != nil {
		data["tls.crt"] = base64.StdEncoding.EncodeToString(crt)
	}
	if key != nil {
		data["tls.key"] = base64.StdEncoding.EncodeToString(key)
	}
	obj := map[string]interface{}{
		"type": secretType,
		"data": data,
	}
	return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
}

func TestParseTLSSecretCertificate(t *testing.T) {
	selfSignedPEM, selfSignedCert, selfSignedKey := generateTestCert(t, genCertOptions{
		subjectCN:  "self-signed.example.com",
		dnsNames:   []string{"self-signed.example.com"},
		selfSigned: true,
	})
	_ = selfSignedCert

	caPEM, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})

	leafPEM, _, leafKey := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com", "*.wild.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})
	_ = leafKey

	chainPEM := append(append([]byte{}, leafPEM...), caPEM...)

	tests := []struct {
		name          string
		secret        RenderableObject
		hostname      string
		want          map[string]interface{}
		checkKeysOnly []string // if set, only assert these keys (for concise cert-content checks)
	}{
		{
			name:     "secret not found",
			secret:   RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}},
			hostname: "",
			want: map[string]interface{}{
				"Exists":          false,
				"WrongType":       false,
				"ActualType":      "",
				"MissingKeys":     []string{},
				"ParseError":      "",
				"SelfSigned":      false,
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"Exists", "WrongType", "ActualType", "MissingKeys", "ParseError", "SelfSigned", "MatchesHostname"},
		},
		{
			name:     "wrong type",
			secret:   tlsSecret("Opaque", selfSignedPEM, keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":     true,
				"WrongType":  true,
				"ActualType": "Opaque",
			},
			checkKeysOnly: []string{"Exists", "WrongType", "ActualType"},
		},
		{
			name:     "missing tls.crt",
			secret:   tlsSecret("kubernetes.io/tls", nil, keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":      true,
				"WrongType":   false,
				"MissingKeys": []string{"tls.crt"},
			},
			checkKeysOnly: []string{"Exists", "WrongType", "MissingKeys"},
		},
		{
			name:     "missing tls.key",
			secret:   tlsSecret("kubernetes.io/tls", selfSignedPEM, nil),
			hostname: "",
			want: map[string]interface{}{
				"Exists":      true,
				"MissingKeys": []string{"tls.key"},
			},
			checkKeysOnly: []string{"Exists", "MissingKeys"},
		},
		{
			name:     "missing both keys",
			secret:   tlsSecret("kubernetes.io/tls", nil, nil),
			hostname: "",
			want: map[string]interface{}{
				"Exists":      true,
				"MissingKeys": []string{"tls.crt", "tls.key"},
			},
			checkKeysOnly: []string{"Exists", "MissingKeys"},
		},
		{
			name:     "malformed base64",
			secret:   tlsSecret("kubernetes.io/tls", []byte("not-valid-base64!!!"), keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists": true,
			},
			checkKeysOnly: []string{"Exists"},
		},
		{
			name:     "malformed pem",
			secret:   tlsSecret("kubernetes.io/tls", []byte("aGVsbG8gd29ybGQ="), keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists": true,
			},
			checkKeysOnly: []string{"Exists"},
		},
		{
			name:     "self-signed cert",
			secret:   tlsSecret("kubernetes.io/tls", selfSignedPEM, keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":          true,
				"WrongType":       false,
				"ParseError":      "",
				"SelfSigned":      true,
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"Exists", "WrongType", "ParseError", "SelfSigned", "MatchesHostname"},
		},
		{
			name:     "CA-signed leaf is not self-signed",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":     true,
				"ParseError": "",
				"SelfSigned": false,
			},
			checkKeysOnly: []string{"Exists", "ParseError", "SelfSigned"},
		},
		{
			name:     "chain: leaf+root concatenated reports leaf's self-signed status, not root's",
			secret:   tlsSecret("kubernetes.io/tls", chainPEM, keyPEMBytes(leafKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":     true,
				"ParseError": "",
				"SelfSigned": false,
			},
			checkKeysOnly: []string{"Exists", "ParseError", "SelfSigned"},
		},
		{
			name:     "hostname match exact",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "leaf.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "hostname mismatch",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "other.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard SAN match",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "foo.wild.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTLSSecretCertificate(tt.secret, tt.hostname)
			for _, key := range tt.checkKeysOnly {
				wantVal, ok := tt.want[key]
				if !ok {
					t.Fatalf("test case %q: missing expected value for key %q", tt.name, key)
				}
				gotVal := got[key]
				if !reflect.DeepEqual(gotVal, wantVal) {
					t.Errorf("parseTLSSecretCertificate()[%q] = %#v, want %#v", key, gotVal, wantVal)
				}
			}
			if tt.name == "malformed base64" || tt.name == "malformed pem" {
				if got["ParseError"] == "" {
					t.Errorf("expected non-empty ParseError for %q", tt.name)
				}
			}
		})
	}

	// Sanity: every key in the result map must always be present (never <no value> in templates).
	all := parseTLSSecretCertificate(RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}}, "")
	expectedKeys := []string{
		"Exists", "WrongType", "ActualType", "MissingKeys", "ParseError",
		"Subject", "Issuer", "SerialNumber", "NotBefore", "NotAfter",
		"DNSNames", "IPAddresses", "KeyAlgorithm", "SelfSigned", "MatchesHostname",
	}
	for _, key := range expectedKeys {
		if _, ok := all[key]; !ok {
			t.Errorf("result map missing key %q", key)
		}
	}
}
