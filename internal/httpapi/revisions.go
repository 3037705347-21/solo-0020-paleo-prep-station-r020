package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) revisePlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.RevisionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.RevisePlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/plans/"+value.PlanID)
	writeJSON(w, http.StatusCreated, value)
}
