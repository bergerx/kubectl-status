package plugin

import "regexp"

// ansiControlSequence matches ANSI/VT100 terminal escape sequences: CSI sequences (ESC [ ... final
// byte), OSC sequences (ESC ] ... terminated by BEL or ST), and other two-byte Fe escapes.
var ansiControlSequence = regexp.MustCompile(
	"\x1b(?:" +
		"\\[[0-?]*[ -/]*[@-~]" + // CSI, e.g. "\x1b[31m"
		"|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)" + // OSC, e.g. "\x1b]0;title\x07"
		"|[@-Z\\\\-_]" + // other Fe escapes, e.g. "\x1bc"
		")")

// otherControlChars matches remaining C0/C1 control characters -- including a bare ESC that
// ansiControlSequence didn't recognize as a full sequence, and CR which can be used to overwrite
// already-printed text -- while leaving tab and newline alone since templates rely on them for
// layout. C0 (\x00-\x1f minus tab/newline) and DEL (\x7f) are ASCII byte escapes; the C1 half
// (U+0080-U+009F) has to be built from a decimal code-point range instead, since raw bytes >=
// 0x80 aren't valid standalone UTF-8 and regexp.MustCompile would reject them.
var otherControlChars = regexp.MustCompile("[\x00-\x08\x0b-\x1f\x7f]|[\U00000080-\U0000009f]")

// sanitizeString strips terminal escape/control sequences from a single string value.
func sanitizeString(s string) string {
	s = ansiControlSequence.ReplaceAllString(s, "")
	s = otherControlChars.ReplaceAllString(s, "")
	return s
}

// sanitizeValue recursively strips terminal escape/control sequences out of string values found
// anywhere inside cluster-supplied data (maps/slices from unstructured content), leaving other
// types untouched.
func sanitizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return sanitizeString(val)
	case map[string]interface{}:
		for k, vv := range val {
			val[k] = sanitizeValue(vv)
		}
		return val
	case []interface{}:
		for i, vv := range val {
			val[i] = sanitizeValue(vv)
		}
		return val
	default:
		return v
	}
}

// sanitizeObject strips terminal escape/control sequences from every string value in obj, in
// place. Applied once at the point cluster data enters rendering (newRenderableObject) so that
// nothing downstream -- any template, any helper -- has to remember to do it: a Condition or
// Event message (or any other free-text field) set by anything with write access in the cluster
// could otherwise embed ANSI sequences to spoof or hide terminal output, the same class of issue
// fixed upstream in kubectl as CVE-2021-25743.
func sanitizeObject(obj map[string]interface{}) map[string]interface{} {
	if obj == nil {
		return obj
	}
	sanitizeValue(obj)
	return obj
}
