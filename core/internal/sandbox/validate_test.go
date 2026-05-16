package sandbox

import (
	"testing"
)

func TestValidateStageName(t *testing.T) {
	tests := []struct {
		name      string
		stageName string
		wantErr   bool
	}{
		// Valid names
		{"simple", "hello", false},
		{"with-dash", "my-stage", false},
		{"with-underscore", "my_stage", false},
		{"alphanumeric", "stage123", false},
		{"mixed", "Stage-1_test", false},

		// Invalid names
		{"empty", "", true},
		{"path-traversal-parent", "..", true},
		{"path-traversal-relative", "../../etc/passwd", true},
		{"path-separator-forward", "stage/subdir", true},
		{"path-separator-back", "stage\\windows", true},
		{"hidden-prefix", "..hidden", true},
		{"special-at", "stage@host", true},
		{"special-dollar", "stage$var", true},
		{"special-space", "stage name", true},
		{"special-colon", "stage:name", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStageName(tt.stageName)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStageName(%q) error = %v, wantErr %v", tt.stageName, err, tt.wantErr)
			}
		})
	}
}
