// Package dto holds the HTTP-edge data transfer objects: request and
// response shapes carrying JSON and validation tags.
//
// Why a separate package from internal/domain:
//
//   - Domain types are wire-shape-agnostic. They have no JSON tags and
//     no validation tags. They describe the world; they don't define
//     how it travels.
//
//   - DTOs translate between the wire shape and the domain shape. A
//     handler reads a CreateTaskRequest (this package), validates it,
//     calls a service with a domain.NewTaskInput, and writes back a
//     TaskResponse (this package).
//
// This phase only creates the package skeleton; Phase 5 fills it with
// CreateTaskRequest, UpdateTaskRequest, TaskResponse, etc.
package dto
