package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// GetRules returns the full chunk array for the frontend rule drawer.
func (h *Handler) GetRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.rules.Chunks())
}

func (h *Handler) GetRule(w http.ResponseWriter, r *http.Request) {
	chunk, ok := h.rules.ChunkByID(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, chunk)
}
