package plugin

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
	"time"
)

// parseTLSSecretCertificate inspects a Secret expected to be type kubernetes.io/tls and
// returns both full certificate detail (for Secret.tmpl's own display) and consistency
// flags against the expected hostname(s) (for Ingress/Gateway callers). hostnames takes either a
// single hostname or a list of them, each of which may be a wildcard pattern such as
// "*.example.com" -- see certServesHostname. A list is satisfied when the certificate serves at
// least one of its entries, since a route with several hostnames only needs the listener's
// certificate to cover the ones it actually terminates. Passing "" (or an empty list) skips the
// hostname-match check and is used by Secret.tmpl, which has no "expected host" of its own.
func (cfg *RenderConfig) parseTLSSecretCertificate(secret RenderableObject, hostnames interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"Exists":          false,
		"WrongType":       false,
		"ActualType":      "",
		"MissingKeys":     []string{},
		"ParseError":      "",
		"Subject":         "",
		"Issuer":          "",
		"SerialNumber":    "",
		"NotBefore":       time.Time{},
		"NotAfter":        time.Time{},
		"DNSNames":        []string{},
		"AltDNSNames":     []string{},
		"IPAddresses":     []string{},
		"KeyAlgorithm":    "",
		"SelfSigned":      false,
		"Expired":         false,
		"MatchesHostname": false,
	}
	if secret.Object == nil {
		return result
	}
	result["Exists"] = true

	actualType, _ := secret.Object["type"].(string)
	result["ActualType"] = actualType
	if actualType != "kubernetes.io/tls" {
		result["WrongType"] = true
		return result
	}

	data, _ := secret.Object["data"].(map[string]interface{})
	var missingKeys []string
	for _, key := range []string{"tls.crt", "tls.key"} {
		if _, ok := data[key]; !ok {
			missingKeys = append(missingKeys, key)
		}
	}
	if missingKeys != nil {
		result["MissingKeys"] = missingKeys
		return result
	}

	crtEncoded, ok := data["tls.crt"].(string)
	if !ok {
		result["ParseError"] = "tls.crt is not a string"
		return result
	}
	crtDecoded, err := base64.StdEncoding.DecodeString(crtEncoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode tls.crt: %v", err)
		return result
	}
	block, _ := pem.Decode(crtDecoded)
	if block == nil {
		result["ParseError"] = "failed to PEM-decode tls.crt"
		return result
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse leaf certificate: %v", err)
		return result
	}

	var ipAddresses []string
	for _, ip := range cert.IPAddresses {
		ipAddresses = append(ipAddresses, ip.String())
	}

	dnsNames := cert.DNSNames
	if dnsNames == nil {
		dnsNames = []string{}
	}
	if ipAddresses == nil {
		ipAddresses = []string{}
	}
	var altDNSNames []string
	for _, dns := range dnsNames {
		if dns != cert.Subject.CommonName {
			altDNSNames = append(altDNSNames, dns)
		}
	}
	if altDNSNames == nil {
		altDNSNames = []string{}
	}

	result["Subject"] = cert.Subject.String()
	result["Issuer"] = cert.Issuer.String()
	result["SerialNumber"] = cert.SerialNumber.String()
	result["NotBefore"] = cert.NotBefore
	result["NotAfter"] = cert.NotAfter
	result["DNSNames"] = dnsNames
	result["AltDNSNames"] = altDNSNames
	result["IPAddresses"] = ipAddresses
	result["KeyAlgorithm"] = cert.PublicKeyAlgorithm.String()
	result["SelfSigned"] = bytes.Equal(cert.RawIssuer, cert.RawSubject)
	result["Expired"] = cert.NotAfter.Before(cfg.Now())

	// Nothing expected to match against (Secret.tmpl, or an unset listener/route hostname) is
	// not a mismatch.
	expected := expectedHostnames(hostnames)
	matchesHostname := len(expected) == 0
	for _, hostname := range expected {
		if certServesHostname(cert, hostname) {
			matchesHostname = true
			break
		}
	}
	result["MatchesHostname"] = matchesHostname

	return result
}

// certServesHostname reports whether cert can serve the hostname a caller expects it to serve.
//
// The expectation comes from an Ingress rule host, a Gateway listener hostname or a route
// hostname, every one of which may itself be a wildcard pattern such as "*.example.com" rather
// than a concrete server name. x509.VerifyHostname is only defined for concrete names: it
// refuses to treat a "*" in its argument as a wildcard and degrades to a byte comparison
// against the SANs, so a "*.example.com" listener fronting a "foo.example.com" (or
// "*.apps.example.com") certificate gets reported as a mismatch even though every name that
// listener accepts routing for is one the certificate covers. For a wildcard expectation we
// therefore ask the weaker, correct question: does the certificate cover *any* hostname this
// caller would accept?
func certServesHostname(cert *x509.Certificate, hostname string) bool {
	certNames := cert.DNSNames
	hasSAN := len(certNames) > 0 || len(cert.IPAddresses) > 0
	if !hasSAN {
		// Pre-RFC-6125 certificates name the server only in the subject CN. Go and browsers
		// ignore it, but OpenSSL-based proxies still honour it, and reporting a hostname
		// mismatch for a certificate whose CN is exactly the expected host misdiagnoses the
		// real problem (the missing SAN) as the wrong one.
		if cn := cert.Subject.CommonName; cn != "" {
			certNames = []string{cn}
		}
	}

	if suffix, isWildcard := wildcardSuffix(hostname); isWildcard {
		for _, certName := range certNames {
			if certNameIntersectsWildcard(certName, suffix) {
				return true
			}
		}
		return false
	}

	if hasSAN {
		return cert.VerifyHostname(hostname) == nil
	}
	for _, certName := range certNames {
		if certNameCoversHost(certName, hostname) {
			return true
		}
	}
	return false
}

// wildcardSuffix splits a "*.example.com" pattern into the "example.com" it is a wildcard for.
func wildcardSuffix(pattern string) (suffix string, isWildcard bool) {
	if !strings.HasPrefix(pattern, "*.") || len(pattern) == len("*.") {
		return "", false
	}
	return pattern[len("*."):], true
}

// certNameIntersectsWildcard reports whether a certificate DNS name covers at least one hostname
// under the "*."+suffix wildcard an Ingress/Gateway/route expects. Both sides can be patterns, so
// this is a set-intersection test rather than a match: a certificate wildcard covers one label
// (RFC 6125), while the expected wildcard covers any depth of subdomain but never the bare
// suffix itself -- "*.example.com" accepts "a.example.com" and "b.a.example.com" but not
// "example.com", per the Gateway API hostname spec and Ingress' wildcard host rules.
func certNameIntersectsWildcard(certName, suffix string) bool {
	base, certIsWildcard := wildcardSuffix(certName)
	if !certIsWildcard {
		base = certName
	}
	if base == "" {
		return false
	}
	// A certificate wildcard "*."+base contributes names one label below base, so it reaches
	// into the expected space as soon as base is the suffix itself. A concrete certificate name
	// has to sit strictly below the suffix.
	if certIsWildcard && strings.EqualFold(base, suffix) {
		return true
	}
	return strings.HasSuffix(strings.ToLower(base), "."+strings.ToLower(suffix))
}

// certNameCoversHost applies RFC 6125 matching -- exact, or a leading wildcard standing for
// exactly one label -- of a single certificate name against a concrete hostname. Only used for
// the legacy CN fallback; SAN matching goes through x509.VerifyHostname.
func certNameCoversHost(certName, host string) bool {
	suffix, isWildcard := wildcardSuffix(certName)
	if !isWildcard {
		return strings.EqualFold(certName, host)
	}
	label, ok := strings.CutSuffix(strings.ToLower(host), "."+strings.ToLower(suffix))
	return ok && label != "" && !strings.Contains(label, ".")
}

// newCertificateEntry returns the zero-value result map for a single ".crt" entry, keyed the
// same way regardless of which resource (Secret or ConfigMap) it was scanned from.
func newCertificateEntry(name string) map[string]interface{} {
	return map[string]interface{}{
		"Name":         name,
		"ParseError":   "",
		"Subject":      "",
		"Issuer":       "",
		"SerialNumber": "",
		"NotBefore":    time.Time{},
		"NotAfter":     time.Time{},
		"AltDNSNames":  []string{},
		"IPAddresses":  []string{},
		"KeyAlgorithm": "",
		"SelfSigned":   false,
		"Expired":      false,
	}
}

// parseCertificateBytesInto PEM-decodes and parses decoded as an X.509 certificate, filling
// entry's fields in place, or setting entry["ParseError"] on failure. name is only used to
// identify the source key in error messages. Expired is computed against cfg.Now() rather than
// time.Now() so it stays pinned under ApplyTestHack.
func (cfg *RenderConfig) parseCertificateBytesInto(entry map[string]interface{}, name string, decoded []byte) {
	block, _ := pem.Decode(decoded)
	if block == nil {
		entry["ParseError"] = fmt.Sprintf("failed to PEM-decode %s", name)
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		entry["ParseError"] = fmt.Sprintf("failed to parse certificate in %s: %v", name, err)
		return
	}

	var ipAddresses []string
	for _, ip := range cert.IPAddresses {
		ipAddresses = append(ipAddresses, ip.String())
	}
	var altDNSNames []string
	for _, dns := range cert.DNSNames {
		if dns != cert.Subject.CommonName {
			altDNSNames = append(altDNSNames, dns)
		}
	}

	entry["Subject"] = cert.Subject.String()
	entry["Issuer"] = cert.Issuer.String()
	entry["SerialNumber"] = cert.SerialNumber.String()
	entry["NotBefore"] = cert.NotBefore
	entry["NotAfter"] = cert.NotAfter
	if len(altDNSNames) > 0 {
		entry["AltDNSNames"] = altDNSNames
	}
	if len(ipAddresses) > 0 {
		entry["IPAddresses"] = ipAddresses
	}
	entry["KeyAlgorithm"] = cert.PublicKeyAlgorithm.String()
	entry["SelfSigned"] = bytes.Equal(cert.RawIssuer, cert.RawSubject)
	entry["Expired"] = cert.NotAfter.Before(cfg.Now())
}

// certificatesInSecret scans a Secret's data for keys ending in ".crt", regardless of the
// Secret's declared type, and parses each as an X.509 certificate. This covers secrets that
// don't use the standard kubernetes.io/tls layout, e.g. cert-manager's internal CA secrets,
// which are type Opaque and hold a ca.crt alongside a tls.crt/tls.key pair.
func (cfg *RenderConfig) certificatesInSecret(secret RenderableObject) []map[string]interface{} {
	var results []map[string]interface{}
	if secret.Object == nil {
		return results
	}
	data, _ := secret.Object["data"].(map[string]interface{})
	if data == nil {
		return results
	}

	var keys []string
	for key := range data {
		if strings.HasSuffix(key, ".crt") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		entry := newCertificateEntry(key)
		results = append(results, entry)

		encoded, ok := data[key].(string)
		if !ok {
			entry["ParseError"] = fmt.Sprintf("%s is not a string", key)
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			entry["ParseError"] = fmt.Sprintf("failed to base64-decode %s: %v", key, err)
			continue
		}
		cfg.parseCertificateBytesInto(entry, key, decoded)
	}

	return results
}

// certificatesInConfigMap scans a ConfigMap's data and binaryData for keys ending in ".crt"
// and parses each as an X.509 certificate. Unlike Secret, ConfigMap.data values are plain
// text (not base64) while ConfigMap.binaryData values are base64, matching the Kubernetes API
// convention for the two fields.
func (cfg *RenderConfig) certificatesInConfigMap(configMap RenderableObject) []map[string]interface{} {
	var results []map[string]interface{}
	if configMap.Object == nil {
		return results
	}

	type source struct {
		key     string
		decoded []byte
		err     error
	}
	var sources []source

	if data, ok := configMap.Object["data"].(map[string]interface{}); ok {
		for key, v := range data {
			if !strings.HasSuffix(key, ".crt") {
				continue
			}
			s, ok := v.(string)
			if !ok {
				sources = append(sources, source{key: key, err: fmt.Errorf("%s is not a string", key)})
				continue
			}
			sources = append(sources, source{key: key, decoded: []byte(s)})
		}
	}
	if binaryData, ok := configMap.Object["binaryData"].(map[string]interface{}); ok {
		for key, v := range binaryData {
			if !strings.HasSuffix(key, ".crt") {
				continue
			}
			s, ok := v.(string)
			if !ok {
				sources = append(sources, source{key: key, err: fmt.Errorf("%s is not a string", key)})
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				sources = append(sources, source{key: key, err: fmt.Errorf("failed to base64-decode %s: %v", key, err)})
				continue
			}
			sources = append(sources, source{key: key, decoded: decoded})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].key < sources[j].key })

	for _, s := range sources {
		entry := newCertificateEntry(s.key)
		results = append(results, entry)
		if s.err != nil {
			entry["ParseError"] = s.err.Error()
			continue
		}
		cfg.parseCertificateBytesInto(entry, s.key, s.decoded)
	}

	return results
}

// certificateInCSR parses a CertificateSigningRequest's status.certificate (base64-encoded PEM,
// populated once a signer issues the certificate) as an X.509 certificate. Returns nil if the
// CSR hasn't been issued yet.
func (cfg *RenderConfig) certificateInCSR(csr RenderableObject) map[string]interface{} {
	certEncoded, ok := csr.Status()["certificate"].(string)
	if !ok || certEncoded == "" {
		return nil
	}

	entry := newCertificateEntry("certificate")
	decoded, err := base64.StdEncoding.DecodeString(certEncoded)
	if err != nil {
		entry["ParseError"] = fmt.Sprintf("failed to base64-decode certificate: %v", err)
		return entry
	}
	cfg.parseCertificateBytesInto(entry, "certificate", decoded)
	return entry
}

// certificateRequestInCSR parses a CertificateSigningRequest's spec.request (a base64-encoded
// PKCS#10 CSR) to surface what's actually being requested -- the subject, SANs, and key
// algorithm -- which is available for Pending and Denied requests too, unlike
// certificateInCSR's status.certificate.
func certificateRequestInCSR(csr RenderableObject) map[string]interface{} {
	result := map[string]interface{}{
		"ParseError":   "",
		"Subject":      "",
		"AltDNSNames":  []string{},
		"IPAddresses":  []string{},
		"KeyAlgorithm": "",
	}

	encoded, ok := csr.Spec()["request"].(string)
	if !ok || encoded == "" {
		result["ParseError"] = "spec.request is empty"
		return result
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to base64-decode request: %v", err)
		return result
	}
	block, _ := pem.Decode(decoded)
	if block == nil {
		result["ParseError"] = "failed to PEM-decode request"
		return result
	}
	csrRequest, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		result["ParseError"] = fmt.Sprintf("failed to parse certificate request: %v", err)
		return result
	}

	var ipAddresses []string
	for _, ip := range csrRequest.IPAddresses {
		ipAddresses = append(ipAddresses, ip.String())
	}
	var altDNSNames []string
	for _, dns := range csrRequest.DNSNames {
		if dns != csrRequest.Subject.CommonName {
			altDNSNames = append(altDNSNames, dns)
		}
	}
	if altDNSNames == nil {
		altDNSNames = []string{}
	}
	if ipAddresses == nil {
		ipAddresses = []string{}
	}

	result["Subject"] = csrRequest.Subject.String()
	result["AltDNSNames"] = altDNSNames
	result["IPAddresses"] = ipAddresses
	result["KeyAlgorithm"] = csrRequest.PublicKeyAlgorithm.String()
	return result
}
