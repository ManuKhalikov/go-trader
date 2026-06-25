package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type stopLossSyncBody struct {
	StrategyID        string  `json:"strategy_id"`
	Symbol            string  `json:"symbol"`
	StopLossTriggerPx float64 `json:"stop_loss_trigger_px,omitempty"`
	StopLossATRMult   float64 `json:"stop_loss_atr_mult,omitempty"`
}

// handleStopLossSyncHTTP re-arms the stop-loss for an open position by calling
// RunHyperliquidUpdateStopLoss. Unlike /protection-sync (TP-only repair),
// this path cancels any stale SL OID and places a fresh reduce-only stop.
//
// Body: { strategy_id, symbol, stop_loss_trigger_px?, stop_loss_atr_mult? }
//
//   - stop_loss_trigger_px wins when > 0
//   - Otherwise: stop_loss_atr_mult * pos.EntryATR computes the trigger from entry
//   - Rejects 422 when EntryATR <= 0 (same guard as /protection-sync)
func (ss *StatusServer) handleStopLossSyncHTTP(w http.ResponseWriter, r *http.Request) {
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

	var body stopLossSyncBody
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

	// Read strategy config under strategiesMu.
	ss.strategiesMu.RLock()
	var sc StrategyConfig
	var scFound bool
	for _, s := range ss.strategies {
		if s.ID == body.StrategyID {
			sc = s
			scFound = true
			break
		}
	}
	ss.strategiesMu.RUnlock()
	if !scFound {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("strategy %q not found", body.StrategyID)})
		return
	}

	// Snapshot position under read lock.
	type posSnap struct {
		qty       float64
		avgCost   float64
		entryATR  float64
		side      string
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
					qty:       pos.Quantity,
					avgCost:   pos.riskAnchorPrice(),
					entryATR:  pos.EntryATR,
					side:      pos.Side,
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

	// Resolve trigger price.
	triggerPx := body.StopLossTriggerPx
	if triggerPx <= 0 {
		// Derive from stop_loss_atr_mult or existing trigger or strategy default.
		mult := body.StopLossATRMult
		if mult <= 0 && sc.StopLossATRMult != nil {
			mult = *sc.StopLossATRMult
		}
		if mult > 0 {
			if snap.entryATR <= 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "EntryATR=0: cannot compute ATR-based SL trigger — pass stop_loss_trigger_px explicitly or retry after ATR is stamped",
				})
				return
			}
			if snap.side == "long" {
				triggerPx = snap.avgCost - mult*snap.entryATR
			} else {
				triggerPx = snap.avgCost + mult*snap.entryATR
			}
		}
		if triggerPx <= 0 && snap.slTrigger > 0 {
			// Re-arm at existing trigger price.
			triggerPx = snap.slTrigger
		}
	}
	if triggerPx <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "cannot determine stop-loss trigger price — pass stop_loss_trigger_px or ensure strategy has stop_loss_atr_mult configured",
		})
		return
	}

	// Cancel+replace SL (no-lock subprocess).
	result, stderr, err := RunHyperliquidUpdateStopLoss(sc.Script, body.Symbol, snap.side, snap.qty, triggerPx, snap.slOID)
	if stderr != "" {
		fmt.Fprintf(os.Stderr, "[stop-loss-sync] stderr: %s\n", stderr)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error": fmt.Sprintf("stop-loss placement failed: %v", err)})
		return
	}
	if result == nil || result.Error != "" {
		errMsg := "nil result from SL update"
		if result != nil {
			errMsg = result.Error
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error": errMsg})
		return
	}

	// Apply OID + trigger to state under write lock.
	var warn string
	if result.StopLossOID > 0 || result.StopLossFilledImmediately {
		ss.mu.Lock()
		if st2 := ss.state.Strategies[body.StrategyID]; st2 != nil {
			if pos2, ok := st2.Positions[snap.symKey]; ok && pos2 != nil && pos2.Quantity > 0 {
				if result.StopLossOID > 0 {
					pos2.StopLossOID = result.StopLossOID
				}
				if result.StopLossTriggerPx > 0 {
					pos2.StopLossTriggerPx = result.StopLossTriggerPx
				} else {
					pos2.StopLossTriggerPx = triggerPx
				}
			}
		}
		if saveErr := SaveStateWithDB(ss.state, ss.configForManualSync(), ss.stateDB); saveErr != nil {
			fmt.Fprintf(os.Stderr, "[stop-loss-sync] state save: %v\n", saveErr)
		}
		ss.mu.Unlock()
	}
	if result.StopLossError != "" {
		warn = result.StopLossError
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":             "ok",
		"stop_loss_oid":      result.StopLossOID,
		"stop_loss_trigger_px": triggerPx,
		"warn":               warn,
	})
}
