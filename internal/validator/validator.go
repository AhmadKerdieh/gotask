// Package validator wraps github.com/go-playground/validator/v10 and
// normalises its output into a map[string]string keyed by field name,
// which is the shape pkg/response.ValidationError expects.
//
// Implementation lands in Phase 2 (Domain Models & Validation).
package validator
