package apperror

import (
	"errors"
	"fmt"
)

// TerminalError marks a failure that retrying cannot fix. Workers use it to
// stop retry loops and to record a stable code in stage/run rows.
type TerminalError struct {
	Code string
	Err  error
}

func (e *TerminalError) Error() string {
	if e.Code == "" {
		if e.Err == nil {
			return "terminal_error"
		}
		return e.Err.Error()
	}
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *TerminalError) Unwrap() error { return e.Err }

// Terminal wraps an existing error as terminal.
func Terminal(code string, err error) error {
	return &TerminalError{Code: code, Err: err}
}

// Terminalf builds a terminal error from a format string.
func Terminalf(code, format string, args ...any) error {
	return &TerminalError{Code: code, Err: fmt.Errorf(format, args...)}
}

func AsTerminal(err error) (*TerminalError, bool) {
	var terminal *TerminalError
	if errors.As(err, &terminal) {
		return terminal, true
	}
	return nil, false
}

func IsTerminal(err error) bool {
	_, ok := AsTerminal(err)
	return ok
}
