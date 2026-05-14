package handler

import (
	"net/http"
	"path/filepath"
)

// Index serves the SPA entry point. Static assets (CSS, JS, images) would be
// served alongside via a FileServer mount; for Phase 1 the page is a single
// self-contained HTML file, so a direct ServeFile is sufficient.
//
// http.ServeFile handles Last-Modified, If-Modified-Since, Content-Type
// detection, and range requests for us.
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(h.cfg.StaticDir, "index.html"))
}
