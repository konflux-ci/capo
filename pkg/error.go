// Error handling for the capo package.
//
// Errors returned from Scanner.Scan are formatted as "[CODE] message" for
// classification by a log aggregator. The code comes from a sentinel error
// (e.g. ErrPullspecResolve = errors.New("ERR_PULLSPEC_RESOLVE")) and the
// message is a human-readable description of what went wrong.
//
// Internally, errors are wrapped with errorf(sentinel, format, args...) which
// pairs the sentinel with a human-readable message without embedding the code
// in the error string. This keeps internal error messages clean and avoids
// formatting the "[CODE] message" string at every call site. The formatting
// is centralized in Scanner.Scan via formatScanError, which walks the error
// chain to find the sentinel and produces a ScanError with the final format.
//
// The sentinel is attached via codedErr.Unwrap() []error, so errors.Is
// continues to work for programmatic error matching throughout the chain.
package capo

import (
	"errors"
	"fmt"
)

// codedErr pairs a sentinel error with a human-readable inner error.
// The sentinel is reachable via Unwrap for errors.Is matching but is
// excluded from Error() so the code never leaks into the message string.
type codedErr struct {
	sentinel error
	inner    error
}

func (e *codedErr) Error() string   { return e.inner.Error() }
func (e *codedErr) Unwrap() []error { return []error{e.sentinel, e.inner} }

// errorf creates a codedErr that tags the error with a sentinel for
// classification while keeping the error string purely descriptive.
// It replaces the pattern fmt.Errorf("%w: ...", sentinel, ...) which
// would bake the sentinel's code string into every error message.
func errorf(sentinel error, format string, args ...any) error {
	return &codedErr{
		sentinel: sentinel,
		inner:    fmt.Errorf(format, args...), //nolint:err113 // sentinel is the static error
	}
}

// ScanError is the error type returned by Scanner.Scan. It formats the
// error as "[CODE] message" and exposes Code and Message as fields so
// consumers can access them programmatically via errors.As.
// The original error chain is preserved via Unwrap for errors.Is matching.
type ScanError struct {
	Code    string
	Message string
	err     error
}

func (e *ScanError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *ScanError) Unwrap() error { return e.err }

// formatScanError walks the error chain to find the outermost codedErr,
// extracts its sentinel code, and wraps everything in a ScanError.
// Errors without a codedErr in the chain get the fallback code ERR_UNKNOWN.
func formatScanError(err error) *ScanError {
	code := "ERR_UNKNOWN"
	var ce *codedErr
	if errors.As(err, &ce) {
		code = ce.sentinel.Error()
	}
	return &ScanError{Code: code, Message: err.Error(), err: err}
}
