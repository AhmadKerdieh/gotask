package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gotask/internal/apperror"
)

// maxRequestBody caps how much we will read from a request body. Without
// this, a hostile client can stream gigabytes and exhaust process memory
// before json.Decode ever fails. 1 MiB is generous for the JSON payloads
// this API accepts; raise it deliberately if a future endpoint needs to.
const maxRequestBody = 1 << 20 // 1 MiB

// decodeJSON is the single strict-decoding entry point every handler
// uses. It enforces, in order:
//
//   - Content-Type must be application/json (415 otherwise). Accepting
//     arbitrary content types and hoping the bytes happen to be JSON is
//     how you get confusing failures.
//   - Body is size-capped via http.MaxBytesReader (413 if exceeded).
//   - Unknown fields are rejected (400). A client typo like "titel"
//     becomes a loud error, not a silently ignored field that leaves the
//     caller wondering why their value did not take effect.
//   - Exactly one JSON value; trailing garbage after it is rejected.
//
// Every failure is mapped to an apperror so the caller's respondError
// produces the standard envelope — a raw json error string is never sent
// to the client. This is the same one-translation-point discipline used
// at every other boundary in the codebase.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	// Content-Type may include parameters, e.g. "application/json; charset=utf-8".
	if mt := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]); mt != "application/json" {
		return apperror.New(
			apperror.KindBadRequest, "unsupported_media_type",
			"Content-Type must be application/json",
		)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}

	// Ensure there is no second JSON value after the first. Decode reads
	// exactly one; a well-behaved client sends exactly one. Anything
	// trailing (other than EOF) means a malformed request.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return apperror.New(
			apperror.KindBadRequest, "bad_request",
			"request body must contain a single JSON object",
		)
	}
	return nil
}

// decodeError turns the various json/decoder error shapes into a clean,
// client-safe apperror. We surface enough to be actionable (which field,
// roughly what went wrong) without echoing internal Go type names.
func decodeError(err error) error {
	var (
		syntaxErr   *json.SyntaxError
		typeErr     *json.UnmarshalTypeError
		maxBytesErr *http.MaxBytesError
	)

	switch {
	case errors.As(err, &maxBytesErr):
		return apperror.New(
			apperror.KindBadRequest, "payload_too_large",
			"request body is too large",
		)
	case errors.As(err, &syntaxErr):
		return apperror.New(
			apperror.KindBadRequest, "bad_request",
			fmt.Sprintf("malformed JSON at byte %d", syntaxErr.Offset),
		)
	case errors.As(err, &typeErr):
		return apperror.New(
			apperror.KindBadRequest, "bad_request",
			fmt.Sprintf("field %q has the wrong type", typeErr.Field),
		)
	case errors.Is(err, io.EOF):
		return apperror.New(
			apperror.KindBadRequest, "bad_request",
			"request body must not be empty",
		)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// The decoder's message is safe and useful here: it names the
		// offending field. Surface it so the client can fix the typo.
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return apperror.New(
			apperror.KindBadRequest, "bad_request",
			"unknown field "+field,
		)
	default:
		return apperror.New(
			apperror.KindBadRequest, "bad_request",
			"request body could not be decoded",
		)
	}
}
