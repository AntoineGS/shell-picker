package sessionipc

import (
	"context"
	"net/http"
	"time"
)

func (server *Server) handleLoadFinalization(response http.ResponseWriter, request *http.Request, body []byte) {
	var wire struct {
		EventID *uint64 `json:"event_id"`
		Applied *bool   `json:"applied"`
	}
	if decodeObject(body, &wire) != nil || wire.EventID == nil || *wire.EventID == 0 || wire.Applied == nil {
		writeError(response, http.StatusBadRequest)
		return
	}
	input := LoadFinalizeRequest{EventID: *wire.EventID, Applied: *wire.Applied}
	if err := server.finalizeLoad(request.Context(), input); err != nil {
		writeBackendError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) finalizeLoad(ctx context.Context, request LoadFinalizeRequest) error {
	if request.EventID == 0 {
		return nil
	}
	finalizer, ok := server.backend.(LoadFinalizer)
	if !ok {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bounded, cancel := requestContext(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	return finalizer.FinalizeLoad(bounded, request)
}
