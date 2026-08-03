package sessionipc

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/AntoineGS/shell-picker/internal/session"
)

func canonicalRoute(request *http.Request) (string, bool) {
	if request.URL.RawQuery != "" || request.URL.RawPath != "" {
		return "", false
	}
	switch request.RequestURI {
	case "/v1/event", "/v1/event/finalize", "/v1/load", "/v1/load/finalize", "/v1/display", "/v1/preview":
		return request.RequestURI, true
	default:
		return "", false
	}
}

func readRequestBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	defer request.Body.Close()
	return io.ReadAll(http.MaxBytesReader(response, request.Body, maxRequestBody))
}

func writeBackendError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrUnknownRecord), errors.Is(err, session.ErrStaleGeneration):
		writeError(response, http.StatusNotFound)
	case errors.Is(err, ErrInvalidLoad), errors.Is(err, session.ErrInvalidEvent), errors.Is(err, session.ErrInvalidNavigation):
		writeError(response, http.StatusBadRequest)
	default:
		writeError(response, http.StatusInternalServerError)
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) error {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	return json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int) {
	message := http.StatusText(status)
	writeJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: message})
}
