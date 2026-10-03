package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) amendPlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.AmendPlanRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	revision, created, err := api.service.AmendPlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	if !created {
		writeJSON(w, http.StatusOK, revision)
		return
	}
	writeJSON(w, http.StatusCreated, revision)
}
