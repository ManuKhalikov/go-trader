package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type reconcileStopLossOIDBody struct {
	StrategyID        string  `json:"strategy_id"`
	Symbol            string  `json:"symbol"`
	StopLossOID       int64   `json:"stop_loss_oid"`
	StopLossTriggerPx float64 `json:"stop_loss_trigger_px,omitempty"`
}

// handleReconcileStopLossOIDHTTP patches go-trader's cached StopLossOID when HL
// already has a resting SL that drifted from virtual state. No Python subprocess.
func (ss *StatusServer) handleReconcileStopLossOIDHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ss.requireManualAuth(w, r) {
		return
	}
	if ss.rejectIfDraining(w) {
		return
	}

	var body reconcileStopLossOIDBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	body.StrategyID = strings.TrimSpace(body.StrategyID)
	body.Symbol = toHyperliquidCoin(strings.TrimSpace(body.Symbol))
	if body.StrategyID == "" || body.Symbol == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "strategy_id and symbol are required"})
		return
	}
	if body.StopLossOID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "stop_loss_oid must be > 0"})
		return
	}

	type posSnap struct {
		slOID     int64
		slTrigger float64
		symKey    string
	}
	var snap posSnap
	var posFound bool

	ss.mu.RLock()
	if st := ss.state.Strategies[body.StrategyID]; st != nil {
		for k, pos := range st.Positions {
			if pos == nil || pos.Quantity <= 0 {
				continue
			}
			sym := toHyperliquidCoin(pos.Symbol)
			if sym == "" {
				sym = body.Symbol
			}
			if sym == body.Symbol {
				snap = posSnap{
					slOID:     pos.StopLossOID,
					slTrigger: pos.StopLossTriggerPx,
					symKey:    k,
				}
				posFound = true
				break
			}
		}
	}
	ss.mu.RUnlock()

	if !posFound {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("no open position for strategy=%q symbol=%q", body.StrategyID, body.Symbol),
		})
		return
	}

	action := "unchanged"
	if snap.slOID != body.StopLossOID || (body.StopLossTriggerPx > 0 && snap.slTrigger != body.StopLossTriggerPx) {
		ss.mu.Lock()
		if st2 := ss.state.Strategies[body.StrategyID]; st2 != nil {
			if pos2, ok := st2.Positions[snap.symKey]; ok && pos2 != nil && pos2.Quantity > 0 {
				pos2.StopLossOID = body.StopLossOID
				if body.StopLossTriggerPx > 0 {
					pos2.StopLossTriggerPx = body.StopLossTriggerPx
				}
			}
		}
		if saveErr := SaveStateWithDB(ss.state, ss.configForManualSync(), ss.stateDB); saveErr != nil {
			fmt.Fprintf(os.Stderr, "[reconcile-stop-loss-oid] state save: %v\n", saveErr)
		}
		ss.mu.Unlock()
		action = "patched"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":             "ok",
		"action":             action,
		"stop_loss_oid":      body.StopLossOID,
		"stop_loss_trigger_px": body.StopLossTriggerPx,
	})
}
