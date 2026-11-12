package api

import (
	"encoding/json"
	"log"
	"net/http"
)

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("json encode: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// internalError loggt den eigentlichen Fehler und antwortet nur mit einer
// generischen 500, damit keine SQL-/Pfad-Details nach aussen gelangen.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %q: %v", r.Method, r.URL.Path, err)
	writeError(w, http.StatusInternalServerError, "interner Fehler")
}

func readJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "ungueltiges JSON: "+err.Error())
		return false
	}
	return true
}
