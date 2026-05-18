// Package dto holds the HTTP-edge data transfer objects: the request and
// response shapes carrying JSON and validation tags.
//
// Why a separate package from internal/domain and internal/service:
//
//   - domain types describe the world. No JSON tags, no validate tags.
//   - service input types describe a business operation's requirements
//     (e.g. status is optional → workflow default applies).
//   - DTOs (this package) describe the WIRE contract: exactly what an
//     HTTP client may send and will receive. They carry json+validate
//     tags and may deliberately differ from the layers beneath — most
//     importantly, a client must not be able to set reporter_id (that is
//     the authenticated user, wired in Phase 6); the request DTO simply
//     has no such field.
//
// The handler is the single place that converts wire <-> service shapes.
// Each conversion is a small, boring, explicit function in this package.
// Boring is the point: it is the seam that lets the schema, the business
// rules, and the API contract change independently of one another.
package dto
