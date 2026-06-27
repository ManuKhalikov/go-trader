package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type protectionSyncBody struct {
	StrategyID      string  `json:"strategy_id"`
	Symbol          string  `json:"symbol"`
	TPTiers         string  `json:"tp_tiers,omitempty"`
	StopLossATRMult float64 `json:"stop_loss_atr_mult,omitempty"`
}

func (ss *StatusServer) handleProtectionSyncHTTP(w http.ResponseWriter, r *http.Request) {
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

	var body protectionSyncBody
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

	// Read strategy config under strategiesMu (independent of mu per CLAUDE.md lock order).
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

	// Snapshot position fields under read lock — avoids holding the lock during
	// the Python subprocess below, which can take several seconds.
	type posSnap struct {
		qty          float64
		avgCost      float64
		entryATR     float64
		side         string
		slOID        int64
		tpOIDs       []int64
		tpArmedTiers []bool
		symKey       string // map key in StrategyState.Positions
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
					qty:          pos.Quantity,
					avgCost:      pos.AvgCost,
					entryATR:     pos.EntryATR,
					side:         pos.Side,
					slOID:        pos.StopLossOID,
					tpOIDs:       cloneInt64s(pos.TPOIDs),
					tpArmedTiers: cloneBools(pos.TPArmedTiers),
					symKey:       k,
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
	if snap.entryATR <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "EntryATR=0: ATR not yet stamped — retry after one scheduler cycle (~2 min)",
		})
		return
	}

	// Early exit when every configured tier is armed or already filled.
	tpTierOverride := parseManualTPTiersJSON(body.TPTiers)
	if protectionTPSatisfied(sc, snap.tpOIDs, snap.tpArmedTiers, tpTierOverride) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"message": "TPs already in place",
			"tp_oids": snap.tpOIDs,
		})
		return
	}

	// Place TPs. slMultForSync=0: SL already armed (stopLossOID>0), placeManualProtectionInline
	// zeroes it to avoid a duplicate stop — pass 0 explicitly so the same logic applies
	// whether or not stopLossOID is set.
	oids, warn, err := placeManualProtectionInline(
		sc, snap.side, snap.qty, snap.avgCost, snap.entryATR, 0, snap.slOID,
		snap.tpOIDs, snap.tpArmedTiers, tpTierOverride,
	)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error": fmt.Sprintf("protection sync failed: %v", err)})
		return
	}

	// Update TPOIDs and TPArmedTiers in state under write lock.
	// Mirrors the update pattern in hyperliquid_protection.go lines 406-425.
	if len(oids) > 0 {
		ss.mu.Lock()
		if st2 := ss.state.Strategies[body.StrategyID]; st2 != nil {
			if pos2, ok := st2.Positions[snap.symKey]; ok && pos2 != nil && pos2.Quantity > 0 {
				pos2.TPOIDs = cloneInt64s(oids)
				if len(pos2.TPArmedTiers) < len(pos2.TPOIDs) {
					extended := make([]bool, len(pos2.TPOIDs))
					copy(extended, pos2.TPArmedTiers)
					pos2.TPArmedTiers = extended
				}
				for i, oid := range pos2.TPOIDs {
					if oid > 0 {
						pos2.TPArmedTiers[i] = true
					}
				}
			}
		}
		if saveErr := SaveStateWithDB(ss.state, ss.configForManualSync(), ss.stateDB); saveErr != nil {
			fmt.Fprintf(os.Stderr, "[protection-sync] state save: %v\n", saveErr)
		}
		ss.mu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"tp_oids": oids,
		"warn":    warn,
	})
}
