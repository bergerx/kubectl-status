package plugin

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

// generateTestCSR generates an in-memory PKCS#10 certificate signing request PEM block.
func generateTestCSR(t *testing.T, subjectCN string, dnsNames []string, ipAddresses []net.IP) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key: %v", err)
	}
	template := &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: subjectCN},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
	}
	derBytes, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("failed to create certificate request: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: derBytes})
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
	cfg := NewRenderConfig(viper.New())
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

	// CommonName deliberately does not come first in DNSNames, to catch any assumption that
	// slicing off DNSNames[0] is equivalent to filtering out the CommonName.
	cnNotFirstPEM, _, cnNotFirstKey := generateTestCert(t, genCertOptions{
		subjectCN:  "cn.example.com",
		dnsNames:   []string{"extra.example.com", "cn.example.com"},
		selfSigned: true,
	})

	// A wildcard certificate as cert-manager issues one, and an apex-only one, to exercise
	// wildcard expected hostnames (Gateway listener/route hostnames and Ingress hosts) on both
	// sides of the comparison.
	wildcardPEM, _, wildcardKey := generateTestCert(t, genCertOptions{
		subjectCN:  "*.example.com",
		dnsNames:   []string{"*.example.com"},
		selfSigned: true,
	})
	apexPEM, _, apexKey := generateTestCert(t, genCertOptions{
		subjectCN:  "example.com",
		dnsNames:   []string{"example.com"},
		selfSigned: true,
	})
	// Pre-RFC-6125 certificate naming the server only in its subject CN, with no SAN extension.
	cnOnlyPEM, _, cnOnlyKey := generateTestCert(t, genCertOptions{
		subjectCN:  "*.cn-only.example.com",
		selfSigned: true,
	})

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
		{
			// A "*.example.com" listener only ever routes names the certificate covers, so
			// flagging this is a false alarm -- x509.VerifyHostname can't tell, since it treats
			// its argument as a concrete server name.
			name:     "wildcard expected hostname is served by a concrete SAN below it",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "*.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname is served by an identical wildcard SAN",
			secret:   tlsSecret("kubernetes.io/tls", wildcardPEM, keyPEMBytes(wildcardKey)),
			hostname: "*.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname is served by a deeper wildcard SAN",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "*.wild.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname mismatch on a sibling subdomain",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "*.other.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname mismatch on an unrelated domain",
			secret:   tlsSecret("kubernetes.io/tls", wildcardPEM, keyPEMBytes(wildcardKey)),
			hostname: "*.example.org",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			// "*.example.com" never routes the apex, so an apex-only certificate serves nothing
			// the listener accepts.
			name:     "wildcard expected hostname is not served by the bare suffix",
			secret:   tlsSecret("kubernetes.io/tls", apexPEM, keyPEMBytes(apexKey)),
			hostname: "*.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			// A certificate wildcard stands for exactly one label (RFC 6125), so "*.example.com"
			// covers no name under "foo.example.com".
			name:     "shallower wildcard SAN doesn't serve a deeper wildcard expected hostname",
			secret:   tlsSecret("kubernetes.io/tls", wildcardPEM, keyPEMBytes(wildcardKey)),
			hostname: "*.foo.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate matches via its CommonName wildcard",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "foo.cn-only.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate matches a wildcard expected hostname",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "*.cn-only.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate wildcard spans a single label only",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "foo.bar.cn-only.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate mismatch",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "foo.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			// The SAN extension is authoritative when present: a certificate with SANs is not
			// rescued by a CommonName that happens to match.
			name:     "CommonName is ignored when the certificate has SANs",
			secret:   tlsSecret("kubernetes.io/tls", cnNotFirstPEM, keyPEMBytes(cnNotFirstKey)),
			hostname: "other.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "AltDNSNames excludes the CommonName regardless of its position in DNSNames",
			secret:   tlsSecret("kubernetes.io/tls", cnNotFirstPEM, keyPEMBytes(cnNotFirstKey)),
			hostname: "",
			want: map[string]interface{}{
				"DNSNames":    []string{"extra.example.com", "cn.example.com"},
				"AltDNSNames": []string{"extra.example.com"},
			},
			checkKeysOnly: []string{"DNSNames", "AltDNSNames"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.parseTLSSecretCertificate(tt.secret, tt.hostname)
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
	all := cfg.parseTLSSecretCertificate(RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}}, "")
	expectedKeys := []string{
		"Exists", "WrongType", "ActualType", "MissingKeys", "ParseError",
		"Subject", "Issuer", "SerialNumber", "NotBefore", "NotAfter",
		"DNSNames", "AltDNSNames", "IPAddresses", "KeyAlgorithm", "SelfSigned", "Expired", "MatchesHostname",
	}
	for _, key := range expectedKeys {
		if _, ok := all[key]; !ok {
			t.Errorf("result map missing key %q", key)
		}
	}
}

// Routes carry a list of hostnames, so the templates hand parseTLSSecretCertificate every
// hostname a listener serves for the route rather than only the single-hostname case they used to
// restrict themselves to.
func TestParseTLSSecretCertificateHostnameLists(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	leafPEM, _, leafKey := generateTestCert(t, genCertOptions{
		subjectCN:  "leaf.example.com",
		dnsNames:   []string{"leaf.example.com", "*.wild.example.com"},
		selfSigned: true,
	})
	secret := tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey))

	tests := []struct {
		name      string
		hostnames interface{}
		want      bool
	}{
		{"empty list expects nothing", []string{}, true},
		{"nil expects nothing", nil, true},
		{"list of empty strings expects nothing", []string{"", ""}, true},
		{"single-entry list matches", []string{"leaf.example.com"}, true},
		{"one served hostname among several is enough", []string{"nope.example.com", "leaf.example.com"}, true},
		{"no served hostname in the list", []string{"nope.example.com", "other.example.com"}, false},
		{"wildcard entry in the list", []string{"*.wild.example.com"}, true},
		// Templates range over unstructured data, so lists reach here as []interface{}.
		{"untyped list from unstructured data", []interface{}{"leaf.example.com"}, true},
		{"plain string is still accepted", "leaf.example.com", true},
		// A hostname with a space in it can't match anything, but must not be split into two
		// hostnames either (which is what cast.ToStringSlice would do to a bare string).
		{"string is not split on whitespace", "nope.example.com leaf.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.parseTLSSecretCertificate(secret, tt.hostnames)["MatchesHostname"]
			if got != tt.want {
				t.Errorf("parseTLSSecretCertificate(secret, %#v)[MatchesHostname] = %v, want %v", tt.hostnames, got, tt.want)
			}
		})
	}
}

func TestCertificatesInSecret(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	caPEM, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})
	leafPEM, _, _ := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})

	t.Run("opaque secret with ca.crt and tls.crt/tls.key", func(t *testing.T) {
		obj := map[string]interface{}{
			"type": "Opaque",
			"data": map[string]interface{}{
				"ca.crt":  base64.StdEncoding.EncodeToString(caPEM),
				"tls.crt": base64.StdEncoding.EncodeToString(leafPEM),
				"tls.key": base64.StdEncoding.EncodeToString([]byte("irrelevant")),
			},
		}
		got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 2 {
			t.Fatalf("expected 2 certificates, got %d: %#v", len(got), got)
		}
		// sorted alphabetically: ca.crt before tls.crt
		if got[0]["Name"] != "ca.crt" || got[0]["SelfSigned"] != true {
			t.Errorf("got[0] = %#v, want ca.crt/self-signed", got[0])
		}
		if got[1]["Name"] != "tls.crt" || got[1]["SelfSigned"] != false {
			t.Errorf("got[1] = %#v, want tls.crt/not self-signed", got[1])
		}
		if got[1]["Issuer"] != caCert.Subject.String() {
			t.Errorf("got[1][Issuer] = %v, want %v", got[1]["Issuer"], caCert.Subject.String())
		}
	})

	t.Run("secret with no .crt fields", func(t *testing.T) {
		obj := map[string]interface{}{
			"type": "Opaque",
			"data": map[string]interface{}{
				"username": base64.StdEncoding.EncodeToString([]byte("admin")),
			},
		}
		got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 0 {
			t.Errorf("expected no certificates, got %#v", got)
		}
	})

	t.Run("malformed cert data reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"type": "Opaque",
			"data": map[string]interface{}{
				"ca.crt": "not-valid-base64!!!",
			},
		}
		got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 || got[0]["ParseError"] == "" {
			t.Errorf("expected 1 entry with non-empty ParseError, got %#v", got)
		}
	})

	if got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}}); len(got) != 0 {
		t.Errorf("expected nil Object to yield no certificates, got %#v", got)
	}
}

func TestCertificatesInConfigMap(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	caPEM, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})
	leafPEM, _, _ := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})

	t.Run("data holds plain-text PEM, not base64", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"ca.crt": string(caPEM),
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 {
			t.Fatalf("expected 1 certificate, got %d: %#v", len(got), got)
		}
		if got[0]["Name"] != "ca.crt" || got[0]["SelfSigned"] != true || got[0]["ParseError"] != "" {
			t.Errorf("got[0] = %#v, want ca.crt/self-signed with no error", got[0])
		}
	})

	t.Run("binaryData holds base64-encoded PEM", func(t *testing.T) {
		obj := map[string]interface{}{
			"binaryData": map[string]interface{}{
				"tls.crt": base64.StdEncoding.EncodeToString(leafPEM),
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 {
			t.Fatalf("expected 1 certificate, got %d: %#v", len(got), got)
		}
		if got[0]["Name"] != "tls.crt" || got[0]["Issuer"] != caCert.Subject.String() {
			t.Errorf("got[0] = %#v, want tls.crt issued by %v", got[0], caCert.Subject.String())
		}
	})

	t.Run("combines data and binaryData, sorted by key", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"ca.crt": string(caPEM),
			},
			"binaryData": map[string]interface{}{
				"tls.crt": base64.StdEncoding.EncodeToString(leafPEM),
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 2 {
			t.Fatalf("expected 2 certificates, got %d: %#v", len(got), got)
		}
		if got[0]["Name"] != "ca.crt" || got[1]["Name"] != "tls.crt" {
			t.Errorf("expected ca.crt before tls.crt, got %#v", got)
		}
	})

	t.Run("configmap with no .crt fields", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"application.properties": "foo=bar",
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 0 {
			t.Errorf("expected no certificates, got %#v", got)
		}
	})

	t.Run("malformed PEM in data reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"ca.crt": "not a certificate",
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 || got[0]["ParseError"] == "" {
			t.Errorf("expected 1 entry with non-empty ParseError, got %#v", got)
		}
	})

	t.Run("malformed base64 in binaryData reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"binaryData": map[string]interface{}{
				"ca.crt": "not-valid-base64!!!",
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 || got[0]["ParseError"] == "" {
			t.Errorf("expected 1 entry with non-empty ParseError, got %#v", got)
		}
	})

	if got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}}); len(got) != 0 {
		t.Errorf("expected nil Object to yield no certificates, got %#v", got)
	}
}

func TestCertificateInCSR(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	_, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})
	leafPEM, _, _ := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})

	t.Run("not yet issued", func(t *testing.T) {
		obj := map[string]interface{}{"status": map[string]interface{}{}}
		got := cfg.certificateInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got != nil {
			t.Errorf("expected nil for unissued CSR, got %#v", got)
		}
	})

	t.Run("issued certificate parses", func(t *testing.T) {
		obj := map[string]interface{}{
			"status": map[string]interface{}{
				"certificate": base64.StdEncoding.EncodeToString(leafPEM),
			},
		}
		got := cfg.certificateInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got == nil {
			t.Fatal("expected non-nil result for issued CSR")
		}
		if got["ParseError"] != "" {
			t.Errorf("unexpected ParseError: %v", got["ParseError"])
		}
		if got["Issuer"] != caCert.Subject.String() {
			t.Errorf("Issuer = %v, want %v", got["Issuer"], caCert.Subject.String())
		}
	})

	t.Run("malformed base64 reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"status": map[string]interface{}{
				"certificate": "not-valid-base64!!!",
			},
		}
		got := cfg.certificateInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got == nil || got["ParseError"] == "" {
			t.Errorf("expected non-nil result with ParseError, got %#v", got)
		}
	})
}

func TestCertificateRequestInCSR(t *testing.T) {
	t.Run("parses subject, SANs and key algorithm", func(t *testing.T) {
		csrPEM := generateTestCSR(t, "my-pod.default.pod.cluster.local",
			[]string{"my-pod.default.pod.cluster.local", "my-svc.default.svc.cluster.local"},
			[]net.IP{net.ParseIP("10.0.0.1")})
		obj := map[string]interface{}{
			"spec": map[string]interface{}{
				"request": base64.StdEncoding.EncodeToString(csrPEM),
			},
		}
		got := certificateRequestInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got["ParseError"] != "" {
			t.Fatalf("unexpected ParseError: %v", got["ParseError"])
		}
		if got["Subject"] != "CN=my-pod.default.pod.cluster.local" {
			t.Errorf("Subject = %v", got["Subject"])
		}
		altDNSNames, _ := got["AltDNSNames"].([]string)
		if len(altDNSNames) != 1 || altDNSNames[0] != "my-svc.default.svc.cluster.local" {
			t.Errorf("AltDNSNames = %#v, want [my-svc.default.svc.cluster.local]", altDNSNames)
		}
		ipAddresses, _ := got["IPAddresses"].([]string)
		if len(ipAddresses) != 1 || ipAddresses[0] != "10.0.0.1" {
			t.Errorf("IPAddresses = %#v, want [10.0.0.1]", ipAddresses)
		}
	})

	t.Run("missing spec.request reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{"spec": map[string]interface{}{}}
		got := certificateRequestInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got["ParseError"] == "" {
			t.Errorf("expected ParseError for missing request, got %#v", got)
		}
	})

	t.Run("malformed base64 reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"spec": map[string]interface{}{
				"request": "not-valid-base64!!!",
			},
		}
		got := certificateRequestInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got["ParseError"] == "" {
			t.Errorf("expected ParseError for malformed base64, got %#v", got)
		}
	})
}
