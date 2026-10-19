package api

import (
	"encoding/json"
	"io"
	"net/http"

	"mindfs/server/internal/api/usecase"
)

func (h *HTTPHandler) handleSessionTitlesSync(w http.ResponseWriter, r *http.Request) {
	var in usecase.SyncSessionTitlesInput
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&in); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid json body"))
		return
	}
	out, err := h.service().SyncSessionTitles(r.Context(), in)
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}
