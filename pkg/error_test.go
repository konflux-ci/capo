//go:build unit

package capo

import (
	"errors"
	"fmt"
	"testing"
)

var errTest = errors.New("ERR_TEST")
var errOther = errors.New("ERR_OTHER")

// TestErrorf_ErrorString verifies that codedErr.Error() returns only the
// human-readable inner message, not the sentinel code.
func TestErrorf_ErrorString(t *testing.T) {
	t.Parallel()
	err := errorf(errTest, "something went wrong")

	got := err.Error()
	want := "something went wrong"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestErrorf_ErrorsIs verifies that errors.Is matches the sentinel
// attached via errorf.
func TestErrorf_ErrorsIs(t *testing.T) {
	t.Parallel()
	err := errorf(errTest, "something went wrong")

	if !errors.Is(err, errTest) {
		t.Error("errors.Is(err, errTest) = false, want true")
	}
}

// TestErrorf_WrapsUnderlying verifies that an underlying error passed via
// %w is reachable through errors.Is and included in the message string.
func TestErrorf_WrapsUnderlying(t *testing.T) {
	t.Parallel()
	underlying := fmt.Errorf("disk full")
	err := errorf(errTest, "write failed: %w", underlying)

	if !errors.Is(err, underlying) {
		t.Error("errors.Is(err, underlying) = false, want true")
	}

	want := "write failed: disk full"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestFormatScanError_Format verifies that formatScanError produces a
// ScanError whose Error() returns "[CODE] message" and whose Code and
// Message fields are set correctly.
func TestFormatScanError_Format(t *testing.T) {
	t.Parallel()
	err := errorf(errTest, "something went wrong")
	se := formatScanError(err)

	want := "[ERR_TEST] something went wrong"
	if got := se.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if se.Code != "ERR_TEST" {
		t.Errorf("Code = %q, want %q", se.Code, "ERR_TEST")
	}
	if se.Message != "something went wrong" {
		t.Errorf("Message = %q, want %q", se.Message, "something went wrong")
	}
}

// TestFormatScanError_PreservesErrorsIs verifies that after wrapping in
// ScanError, the original sentinel is still reachable via errors.Is.
func TestFormatScanError_PreservesErrorsIs(t *testing.T) {
	t.Parallel()
	err := errorf(errTest, "something went wrong")
	se := formatScanError(err)

	if !errors.Is(se, errTest) {
		t.Error("errors.Is(se, errTest) = false, want true")
	}
	if errors.Is(se, errOther) {
		t.Error("errors.Is(se, errOther) = true, want false")
	}
}

// TestFormatScanError_WrappedInFmtErrorf verifies that formatScanError
// finds the sentinel even when the codedErr is wrapped by a plain
// fmt.Errorf (as happens at Scan call sites that add context without a
// new sentinel).
func TestFormatScanError_WrappedInFmtErrorf(t *testing.T) {
	t.Parallel()
	inner := errorf(errTest, "low-level failure")
	wrapped := fmt.Errorf("added context: %w", inner)
	se := formatScanError(wrapped)

	if se.Code != "ERR_TEST" {
		t.Errorf("Code = %q, want %q", se.Code, "ERR_TEST")
	}

	want := "[ERR_TEST] added context: low-level failure"
	if got := se.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(se, errTest) {
		t.Error("errors.Is(se, errTest) = false, want true")
	}
}

// TestFormatScanError_NoSentinel verifies that an error without any
// codedErr in its chain gets the fallback code ERR_UNKNOWN.
func TestFormatScanError_NoSentinel(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("plain error")
	se := formatScanError(err)

	if se.Code != "ERR_UNKNOWN" {
		t.Errorf("Code = %q, want %q", se.Code, "ERR_UNKNOWN")
	}

	want := "[ERR_UNKNOWN] plain error"
	if got := se.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestScanError_ErrorsAs verifies that consumers can use errors.As to
// extract the *ScanError and access Code and Message programmatically.
func TestScanError_ErrorsAs(t *testing.T) {
	t.Parallel()
	err := errorf(errTest, "something went wrong")
	se := formatScanError(err)

	var target *ScanError
	if !errors.As(se, &target) {
		t.Fatal("errors.As(se, &ScanError) = false, want true")
	}
	if target.Code != "ERR_TEST" {
		t.Errorf("Code = %q, want %q", target.Code, "ERR_TEST")
	}
	if target.Message != "something went wrong" {
		t.Errorf("Message = %q, want %q", target.Message, "something went wrong")
	}
}
