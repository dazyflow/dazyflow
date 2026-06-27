package daemon

import (
	"net/http"
	"testing"
)

func TestRenderAssist_DecodeError(t *testing.T) {
	h := newGatewayHarness(t)
	req := newRawReq(t, h, "POST", "/api/v1/tools/render-template/assist", "{not json")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("assist bad json = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}
