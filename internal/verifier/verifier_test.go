package verifier

import "testing"

func TestVerifyRequiresEveryObservation(t *testing.T) {
	tests := []struct {
		name, before, after, output string
		cleaned, want               bool
	}{
		{"observed", "abc", "abc", "HOST_PATH_NOT_VISIBLE\n", true, true},
		{"changed canary", "abc", "different", "HOST_PATH_NOT_VISIBLE\n", true, false},
		{"missing marker", "abc", "abc", "", true, false},
		{"not cleaned", "abc", "abc", "HOST_PATH_NOT_VISIBLE\n", false, false},
		{"empty hashes", "", "", "HOST_PATH_NOT_VISIBLE\n", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify(tt.before, tt.after, tt.output, tt.cleaned); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
