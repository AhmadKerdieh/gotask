// Package validator wraps github.com/go-playground/validator/v10 and
// normalises its output into a map[string]string keyed by JSON field name.
//
// Why a wrapper instead of using the library directly:
//
//  1. The raw library returns validator.ValidationErrors — a slice of
//     interface values that handlers would have to type-switch on. We
//     flatten that to a flat map keyed by field name, which is the shape
//     apperror.Validation expects (from Phase 1).
//
//  2. We register custom validators that consult the workflow config
//     (status, priority). The validator is the natural place for them
//     because struct-tag validation is what handlers already pay for —
//     adding "validate:\"status\"" to a DTO field is free at every call
//     site.
//
//  3. By default the library reports errors by Go field name (e.g.
//     "Title"). API clients see JSON field names ("title"), so we install
//     a name function that pulls the JSON tag and reports that instead.
//
// The Validator type is safe for concurrent use; the underlying library
// is too. Construct one at startup with New and share it.
package validator

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	pv "github.com/go-playground/validator/v10"

	"gotask/internal/config"
)

// Validator wraps a *pv.Validate and a reference to the workflow config
// for the custom rules.
type Validator struct {
	v        *pv.Validate
	workflow *config.Workflow
}

// New constructs a Validator that consults the given workflow.
//
// The workflow reference is held, not copied: if a future phase hot-
// reloads workflow.yaml by swapping pointers under a lock, this validator
// can be updated to follow that pointer. For now the reference is stable
// for the life of the process.
func New(wf *config.Workflow) *Validator {
	v := pv.New(pv.WithRequiredStructEnabled())

	// Report field names using the JSON tag instead of the Go struct field
	// name. The library lets you plug in any function from
	// reflect.StructField → string.
	//
	// This is the single biggest reason handlers don't need to translate
	// validator output: "Title" → "title" happens here, once.
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		tag := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if tag == "" || tag == "-" {
			return fld.Name
		}
		return tag
	})

	val := &Validator{v: v, workflow: wf}

	// Register custom validators. The closure captures `val` so each rule
	// can read the (current) workflow pointer at validation time.
	_ = v.RegisterValidation("status", val.statusRule)
	_ = v.RegisterValidation("priority", val.priorityRule)

	return val
}

// Struct validates the given struct. Returns nil on success, or a map of
// field-name → human-readable message on failure.
//
// The shape of the returned map matches what apperror.Validation expects;
// handlers can hand the map straight to it:
//
//	if fields := h.validator.Struct(req); fields != nil {
//	    h.respondError(w, r, apperror.Validation(fields))
//	    return
//	}
func (val *Validator) Struct(s any) map[string]string {
	err := val.v.Struct(s)
	if err == nil {
		return nil
	}

	// InvalidValidationError is returned when the input itself is invalid
	// (e.g. a nil pointer was passed). That's a programming error — not
	// something the client did — so surface it loudly rather than as a
	// field-level message.
	var invalid *pv.InvalidValidationError
	if errors.As(err, &invalid) {
		return map[string]string{"_error": invalid.Error()}
	}

	var ves pv.ValidationErrors
	if !errors.As(err, &ves) {
		return map[string]string{"_error": err.Error()}
	}

	out := make(map[string]string, len(ves))
	for _, fe := range ves {
		out[fe.Field()] = humanMessage(fe)
	}
	return out
}

// statusRule implements the "status" tag — string must be a known status
// in the workflow. The validator passes the field's reflect.Value; we
// only support string-typed fields (which includes domain.Status because
// it's a typed string alias and reflect treats it as Kind == String).
func (val *Validator) statusRule(fl pv.FieldLevel) bool {
	if val.workflow == nil {
		return false
	}
	s, ok := stringValue(fl)
	if !ok {
		return false
	}
	return val.workflow.IsStatus(s)
}

// priorityRule implements the "priority" tag — same shape as statusRule.
func (val *Validator) priorityRule(fl pv.FieldLevel) bool {
	if val.workflow == nil {
		return false
	}
	s, ok := stringValue(fl)
	if !ok {
		return false
	}
	return val.workflow.IsPriority(s)
}

// stringValue extracts the underlying string from a reflect.Value, handling
// both `string` and typed-string-alias fields (domain.Status, domain.Priority).
// Returns false for any other kind.
func stringValue(fl pv.FieldLevel) (string, bool) {
	v := fl.Field()
	if v.Kind() != reflect.String {
		return "", false
	}
	return v.String(), true
}

// humanMessage converts one FieldError into a human-readable message.
// Add cases as new rules are introduced. Keep messages action-oriented
// ("must be at least 1 character") rather than rule-jargon
// ("failed 'min' tag with parameter '1'").
//
// The default case falls back to the rule tag so we never silently lose
// information, even if a rule slips in without a tailored message.
func humanMessage(fe pv.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "required"
	case "min":
		return fmt.Sprintf("must be at least %s characters", fe.Param())
	case "max":
		return fmt.Sprintf("must be at most %s characters", fe.Param())
	case "email":
		return "must be a valid email address"
	case "uuid":
		return "must be a valid UUID"
	case "oneof":
		return fmt.Sprintf("must be one of: %s", strings.ReplaceAll(fe.Param(), " ", ", "))
	case "status":
		return "must be a known status (see workflow.yaml)"
	case "priority":
		return "must be a known priority (see workflow.yaml)"
	default:
		return fmt.Sprintf("failed %q validation", fe.Tag())
	}
}
