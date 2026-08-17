package plugin

import "testing"

func TestSanitizeString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "container failed to start", "container failed to start"},
		{"newline and tab preserved", "line1\n\tindented", "line1\n\tindented"},
		{"CSI color sequence stripped", "\x1b[31mCrashLoopBackOff\x1b[0m", "CrashLoopBackOff"},
		{"OSC sequence stripped", "\x1b]0;fake title\x07visible", "visible"},
		{"carriage return stripped", "real message\rFAKE OVERWRITE", "real messageFAKE OVERWRITE"},
		{"bare ESC not forming a full sequence is stripped", "abc\x1bdef", "abcdef"},
		{"C0 control chars stripped", "a\x00b\x07c", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeString(tt.in); got != tt.want {
				t.Errorf("sanitizeString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeObject(t *testing.T) {
	obj := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"message": "\x1b[2K\rReady\x1b[0m",
				},
			},
		},
		"count": float64(3),
	}
	sanitizeObject(obj)
	conditions := obj["status"].(map[string]interface{})["conditions"].([]interface{})
	msg := conditions[0].(map[string]interface{})["message"]
	if msg != "Ready" {
		t.Errorf("nested message = %q, want %q", msg, "Ready")
	}
	if obj["count"] != float64(3) {
		t.Errorf("non-string value was mutated: %v", obj["count"])
	}
}
