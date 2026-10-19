package inputmethod

import (
	"strings"
	"testing"
)

func TestValidateSelection(t *testing.T) {
	for _, method := range []Method{{}, {ID: "macos:com.example.input", Name: "Example IME"}, {ID: "windows:profile"}} {
		if err := ValidateSelection(method.ID, method.Name); err != nil {
			t.Fatalf("valid selection rejected: %v", err)
		}
	}
	for _, method := range []Method{{Name: "orphan"}, {ID: "id\nother"}, {ID: "id", Name: "bad\x00name"}, {ID: strings.Repeat("x", 513)}, {ID: "id", Name: strings.Repeat("x", 257)}} {
		if err := ValidateSelection(method.ID, method.Name); err == nil {
			t.Fatalf("invalid selection accepted: %+v", method)
		}
	}
}
