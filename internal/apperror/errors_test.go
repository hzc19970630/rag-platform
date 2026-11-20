package apperror

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidationErrorRoundTrip(t *testing.T) {
	inner := errors.New("name is required")
	validation := &ValidationError{Err: inner}

	if got := validation.Error(); got != inner.Error() {
		t.Fatalf("Error() = %q, want %q", got, inner.Error())
	}
	if !errors.Is(validation, inner) {
		t.Fatal("ValidationError must unwrap to its cause")
	}
	if got, ok := AsValidation(validation); !ok || got != validation {
		t.Fatalf("AsValidation(validation) = %v, %v", got, ok)
	}
	if got, ok := AsValidation(inner); ok || got != nil {
		t.Fatalf("AsValidation(plain error) = %v, %v", got, ok)
	}

	built := Invalidf("field %q is invalid", "name")
	fromBuilt, ok := AsValidation(built)
	if !ok || !strings.Contains(fromBuilt.Error(), `"name" is invalid`) {
		t.Fatalf("Invalidf round trip = %v, %v", fromBuilt, ok)
	}
}

func TestTerminalErrorFormattingAndUnwrap(t *testing.T) {
	cases := []struct {
		name string
		err  *TerminalError
		want string
	}{
		{name: "no code or cause", err: &TerminalError{}, want: "terminal_error"},
		{name: "cause only", err: &TerminalError{Err: errors.New("boom")}, want: "boom"},
		{name: "code only", err: &TerminalError{Code: "bad_input"}, want: "bad_input"},
		{name: "code and cause", err: &TerminalError{Code: "bad_input", Err: errors.New("boom")}, want: "bad_input: boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}

	cause := errors.New("root cause")
	wrapped := Terminal("bad_input", cause)
	if !errors.Is(wrapped, cause) {
		t.Fatal("Terminal must unwrap to its cause")
	}
	if got, ok := AsTerminal(wrapped); !ok || got.Code != "bad_input" {
		t.Fatalf("AsTerminal(wrapped) = %v, %v", got, ok)
	}
	if !IsTerminal(wrapped) {
		t.Fatal("IsTerminal(wrapped) = false")
	}
	if IsTerminal(cause) {
		t.Fatal("IsTerminal(cause) = true")
	}

	formatted := Terminalf("bad_input", "limit %d exceeded", 5)
	if got := formatted.Error(); got != "bad_input: limit 5 exceeded" {
		t.Fatalf("Terminalf Error() = %q", got)
	}
}

func TestSentinelErrorsAreDistinctAndWrappable(t *testing.T) {
	sentinels := []error{
		ErrUnauthenticated,
		ErrForbidden,
		ErrNotFound,
		ErrConflict,
		ErrNotReady,
		ErrRateLimited,
		ErrQuotaExceeded,
		ErrContentUnavailable,
		ErrVersionSuperseded,
	}
	seen := make(map[string]bool, len(sentinels))
	for _, err := range sentinels {
		if err == nil {
			t.Fatal("sentinel error is nil")
		}
		if seen[err.Error()] {
			t.Fatalf("duplicate sentinel message %q", err.Error())
		}
		seen[err.Error()] = true
		if wrapped := fmt.Errorf("wrapped: %w", err); !errors.Is(wrapped, err) {
			t.Fatalf("sentinel %q is not wrappable", err)
		}
	}
	if got := ErrVersionSuperseded.Error(); got != "version_superseded" {
		t.Fatalf("ErrVersionSuperseded = %q", got)
	}
}

func TestErrorCodesAreStableAndDistinct(t *testing.T) {
	codes := []string{
		CodeInvalidRequest,
		CodeUnauthenticated,
		CodeForbidden,
		CodeNotFound,
		CodeConflict,
		CodeNotReady,
		CodeRateLimited,
		CodeQuotaExceeded,
		CodeContentUnavailable,
		CodePayloadTooLarge,
		CodeInternal,
	}
	seen := make(map[string]bool, len(codes))
	for _, code := range codes {
		if code == "" {
			t.Fatal("empty error code")
		}
		if seen[code] {
			t.Fatalf("duplicate error code %q", code)
		}
		seen[code] = true
	}
}
