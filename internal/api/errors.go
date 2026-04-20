package api

import (
	"encoding/json"
	"net/http"

	"github.com/vincentiuslienardo/selatpay/internal/api/apispec"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apispec.Error{Code: code, Message: msg})
}
