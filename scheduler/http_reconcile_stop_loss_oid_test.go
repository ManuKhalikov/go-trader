package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleReconcileStopLossOIDHTTP_PatchesState(t *testing.T) {
	ss := &StatusServer{
		state: &AppState{
			Strategies: map[string]*StrategyState{
				"hl-roundtable": {
					Positions: map[string]*Position{
						"BTC": {
							Symbol:            "BTC",
							Side:              "long",
							Quantity:          0.01,
							StopLossOID:       1111,
							StopLossTriggerPx: 62000,
						},
					},
				},
			},
		},
	}
	body, _ := json.Marshal(map[string]any{
		"strategy_id":          "hl-roundtable",
		"symbol":               "BTC",
		"stop_loss_oid":        8888,
		"stop_loss_trigger_px": 7303.3,
	})
	req := httptest.NewRequest(http.MethodPost, "/reconcile-stop-loss-oid", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	ss.handleReconcileStopLossOIDHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["action"] != "patched" {
		t.Fatalf("action = %v want patched", resp["action"])
	}
	pos := ss.state.Strategies["hl-roundtable"].Positions["BTC"]
	if pos.StopLossOID != 8888 {
		t.Fatalf("StopLossOID = %d want 8888", pos.StopLossOID)
	}
	if pos.StopLossTriggerPx != 7303.3 {
		t.Fatalf("StopLossTriggerPx = %g want 7303.3", pos.StopLossTriggerPx)
	}
}
