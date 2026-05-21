package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"gotask/internal/apperror"
	"gotask/internal/audit"
	"gotask/internal/authz"
	"gotask/internal/handler/dto"
	"gotask/internal/keycloakadmin"
	"gotask/pkg/response"
)

// ListAudit handles GET /api/v1/audit.
//
// Authorization rules in plain English:
//
//   - Members and managers see ONLY their own audit history. The
//     handler restricts the query to their subject regardless of any
//     ?actor= query parameter. Quietly forcing the filter (vs. 403'ing
//     on attempted ?actor=) avoids two distinct failure modes for the
//     same intent and keeps the UI simple.
//
//   - Admins may pass ?actor=<uuid> to see anyone's history, OR omit
//     it for the cross-user view. authz.CanListAuditAcrossUsers is the
//     gate.
//
// Query params: ?actor=<uuid>, ?action=<string>, ?limit=<int>.
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authedClaims(r)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	q := audit.Query{
		Action: r.URL.Query().Get("action"),
	}

	if authz.CanListAuditAcrossUsers(claims) {
		// Admin: actor filter is optional.
		if v := r.URL.Query().Get("actor"); v != "" {
			id, perr := uuid.Parse(v)
			if perr != nil {
				h.respondError(w, r, apperror.BadRequest("actor must be a valid UUID"))
				return
			}
			q.Actor = id
		}
	} else {
		// Non-admins are constrained to their own history. We do NOT
		// honour ?actor= for them — even if they passed it, the filter
		// is overwritten with their own subject. This is the right
		// "fail open" for a UI that may forward params it doesn't
		// understand.
		q.Actor = authz.Subject(claims)
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			q.Limit = n
		}
	}

	entries, err := h.auditor.List(r.Context(), q)
	if err != nil {
		h.respondError(w, r, err)
		return
	}

	// Enrich with names from Keycloak via the lookup. The resolver is
	// scoped to this request so its in-memory dedupe is bounded; the
	// underlying client cache lives longer.
	resolver := &lookupResolver{ctx: r.Context(), lookup: h.userLookup}
	response.OK(w, http.StatusOK, dto.NewAuditListResponse(entries, resolver))
}

// lookupResolver adapts a keycloakadmin.Lookup to dto.UserResolver.
// It is created per request (so the request's context, with its
// timeout/cancellation, governs the calls) and tolerates a nil lookup
// (returns blanks — the degraded-but-working path).
type lookupResolver struct {
	ctx    context.Context
	lookup keycloakadmin.Lookup
}

func (l *lookupResolver) Resolve(sub uuid.UUID) (string, string) {
	if l.lookup == nil {
		return "", ""
	}
	info, err := l.lookup.GetUser(l.ctx, sub)
	if err != nil {
		// Lookup failure is non-fatal — we return blanks and the wire
		// response shows the subject only. Audit responses are best-
		// effort enrichment; the local audit_log row is authoritative.
		return "", ""
	}
	return info.Name, info.Email
}
