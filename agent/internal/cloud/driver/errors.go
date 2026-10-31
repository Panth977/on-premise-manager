package driver

import (
	"errors"
	"fmt"
	"strings"
)

// Store error statuses (gRPC-style, StoreErrorCode of the TS port).
const (
	AlreadyExists      = "already-exists"
	NotFound           = "not-found"
	Aborted            = "aborted"
	FailedPrecondition = "failed-precondition"
	InvalidArgument    = "invalid-argument"
	Unavailable        = "unavailable"
)

var storeNum = map[string]int{AlreadyExists: 6, NotFound: 5, Aborted: 10, FailedPrecondition: 9, InvalidArgument: 3, Unavailable: 14}

// StoreError is the document store's error.
type StoreError struct {
	Status  string
	Code    int
	Message string
}

func (e *StoreError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Code, strings.ToUpper(strings.ReplaceAll(e.Status, "-", "_")), e.Message)
}

func storeErr(status, msg string) *StoreError {
	return &StoreError{Status: status, Code: storeNum[status], Message: msg}
}
func bad(format string, a ...any) *StoreError {
	return storeErr(InvalidArgument, fmt.Sprintf(format, a...))
}

// StoreErrorCode is storeErrorCode: the status of a store error, "" for anything else.
func StoreErrorCode(err error) string {
	var e *StoreError
	if errors.As(err, &e) {
		return e.Status
	}
	return ""
}

// IsAlreadyExists is isAlreadyExists of fire.ts.
func IsAlreadyExists(err error) bool { return StoreErrorCode(err) == AlreadyExists }

// AuthError is the auth port's error (code auth/…).
type AuthError struct {
	Code    string
	Message string
}

func (e *AuthError) Error() string { return e.Message }

// AuthErrorCode returns the auth/… code of an error, "" for anything else.
func AuthErrorCode(err error) string {
	var e *AuthError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
