package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// hlFetchedPosition is a position from fetch_hl_open_positions.py.
type hlFetchedPosition struct {
	Coin          string  `json:"coin"`
	Size          float64 `json:"size"`           // negative for shorts
	EntryPrice    float64 `json:"entry_price"`
	UnrealizedPnl float64 `json:"unrealized_pnl"`
}

// hlFetchedOrder is an open order from fetch_hl_open_positions.py.
type hlFetchedOrder struct {
	Coin             string  `json:"coin"`
	OID              int64   `json:"oid"`
	Side             string  `json:"side"`       // "A" = sell/ask, "B" = buy/bid
	Sz               float64 `json:"sz"`
	IsTrigger        bool    `json:"is_trigger"`
	TriggerPx        float64 `json:"trigger_px"`
	TriggerCondition string  `json:"trigger_condition"`
	OrderType        string  `json:"order_type"`
	ReduceOnly       bool    `json:"reduce_only"`
}

type hlFetchedWallet struct {
	Positions  []hlFetchedPosition `json:"positions"`
	OpenOrders []hlFetchedOrder    `json:"open_orders"`
	Error      string              `json:"error,omitempty"`
}

type adoptHLPositionResult struct {
	StrategyID string  `json:"strategy_id,omitempty"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side,omitempty"`
	Quantity   float64 `json:"quantity,omitempty"`
	// Action values: adopted | sl_oid_patched | already_tracked | sl_oid_missing | no_strategy | adopt_failed
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

type adoptHLPositionsResponse struct {
	Results []adoptHLPositionResult `json:"results"`
	Error   string                  `json:"error,omitempty"`
}

// findSLOrder returns the matching reduce-only trigger order for the given
// position coin+side, or nil if none found.
// Long SL closes with a sell (side "A"); short SL closes with a buy (side "B").
func findSLOrder(coin, posSide string, orders []hlFetchedOrder) *hlFetchedOrder {
	bareCoin := normalizeHlCoin(coin)
	wantSide := "A"
	if posSide == "short" {
		wantSide = "B"
	}
	for i := range orders {
		o := &orders[i]
		if !o.IsTrigger || !o.ReduceOnly || o.OID <= 0 {
			continue
		}
		if normalizeHlCoin(o.Coin) != bareCoin {
			continue
		}
		if strings.ToUpper(o.Side) != wantSide {
			continue
		}
		return o
	}
	return nil
}

// handleAdoptHLPositionsHTTP fetches all open HL positions for the wallet,
// matches each against a configured type=manual strategy, and either:
//   - patches a missing StopLossOID when the position is already in state, or
//   - inserts a pending_manual_action to adopt a position go-trader doesn't know about.
//
// POST /adopt-hl-positions
// Body (optional): {"dry_run": true}
func (ss *StatusServer) handleAdoptHLPositionsHTTP(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		DryRun bool `json:"dry_run"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// Snapshot strategy configs (no state lock needed — strategiesMu is separate).
	ss.strategiesMu.RLock()
	strategies := append([]StrategyConfig(nil), ss.strategies...)
	ss.strategiesMu.RUnlock()

	type manualEntry struct {
		sc   StrategyConfig
		coin string // canonical coin as stored in strategy state (Args[1])
	}
	manualByCoin := make(map[string]manualEntry)
	for _, sc := range strategies {
		if sc.Type != "manual" {
			continue
		}
		coin := hyperliquidSymbol(sc.Args)
		if coin == "" {
			continue
		}
		manualByCoin[normalizeHlCoin(coin)] = manualEntry{sc: sc, coin: coin}
	}

	// Fetch on-chain positions + orders (no lock held during subprocess).
	stdout, stderr, runErr := runPythonReadOnly("shared_scripts/fetch_hl_open_positions.py", nil)
	if len(stderr) > 0 {
		fmt.Fprintf(os.Stderr, "[adopt-hl-positions] fetch stderr: %s\n", string(stderr))
	}
	if runErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(adoptHLPositionsResponse{Error: fmt.Sprintf("fetch_hl_open_positions.py: %v", runErr)})
		return
	}
	var wallet hlFetchedWallet
	if err := json.Unmarshal(stdout, &wallet); err != nil || wallet.Error != "" {
		errMsg := fmt.Sprintf("parse output: %v", err)
		if wallet.Error != "" {
			errMsg = wallet.Error
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(adoptHLPositionsResponse{Error: errMsg})
		return
	}

	var results []adoptHLPositionResult
	var pendingInserted int

	for _, hlPos := range wallet.Positions {
		bareCoin := normalizeHlCoin(hlPos.Coin)
		entry, hasStrategy := manualByCoin[bareCoin]
		if !hasStrategy {
			results = append(results, adoptHLPositionResult{
				Symbol: hlPos.Coin,
				Action: "no_strategy",
				Note:   "no type=manual strategy configured for this coin",
			})
			continue
		}

		qty := math.Abs(hlPos.Size)
		side := "long"
		if hlPos.Size < 0 {
			side = "short"
		}

		slOrder := findSLOrder(hlPos.Coin, side, wallet.OpenOrders)

		// Read current state for this strategy (read lock only).
		type stateSnap struct {
			hasPos    bool
			slOID     int64
			slTrigger float64
		}
		var snap stateSnap
		ss.mu.RLock()
		if st := ss.state.Strategies[entry.sc.ID]; st != nil {
			if pos := st.Positions[entry.coin]; pos != nil && pos.Quantity > 0 {
				snap.hasPos = true
				snap.slOID = pos.StopLossOID
				snap.slTrigger = pos.StopLossTriggerPx
			}
		}
		ss.mu.RUnlock()

		if snap.hasPos {
			if snap.slOID > 0 {
				results = append(results, adoptHLPositionResult{
					StrategyID: entry.sc.ID,
					Symbol:     hlPos.Coin,
					Side:       side,
					Quantity:   qty,
					Action:     "already_tracked",
					Note:       fmt.Sprintf("SL OID %d @ $%.4f", snap.slOID, snap.slTrigger),
				})
				continue
			}
			// Position in state but SL OID = 0 — patch from HL orders.
			if slOrder == nil {
				results = append(results, adoptHLPositionResult{
					StrategyID: entry.sc.ID,
					Symbol:     hlPos.Coin,
					Side:       side,
					Quantity:   qty,
					Action:     "sl_oid_missing",
					Note:       "no matching reduce-only trigger found on HL",
				})
				continue
			}
			note := fmt.Sprintf("patched OID %d @ $%.4f", slOrder.OID, slOrder.TriggerPx)
			if req.DryRun {
				note = "dry_run: would " + note
			} else {
				ss.mu.Lock()
				if st := ss.state.Strategies[entry.sc.ID]; st != nil {
					if pos := st.Positions[entry.coin]; pos != nil && pos.Quantity > 0 {
						pos.StopLossOID = slOrder.OID
						pos.StopLossTriggerPx = slOrder.TriggerPx
					}
				}
				_ = SaveStateWithDB(ss.state, ss.configForManualSync(), ss.stateDB)
				ss.mu.Unlock()
			}
			results = append(results, adoptHLPositionResult{
				StrategyID: entry.sc.ID,
				Symbol:     hlPos.Coin,
				Side:       side,
				Quantity:   qty,
				Action:     "sl_oid_patched",
				Note:       note,
			})
			continue
		}

		// Position not in go-trader state — adopt via pending_manual_actions.
		var slOID int64
		var slTrigger float64
		if slOrder != nil {
			slOID = slOrder.OID
			slTrigger = slOrder.TriggerPx
		}
		note := fmt.Sprintf("entry $%.4f qty=%g %s", hlPos.EntryPrice, qty, side)
		if slOID > 0 {
			note += fmt.Sprintf(" | SL OID %d @ $%.4f", slOID, slTrigger)
		}
		if req.DryRun {
			note = "dry_run: would adopt — " + note
		} else {
			err := ss.stateDB.InsertPendingManualAction(PendingManualAction{
				StrategyID:        entry.sc.ID,
				Action:            "open",
				Symbol:            entry.coin,
				Side:              side,
				Quantity:          qty,
				FillPrice:         hlPos.EntryPrice,
				StopLossOID:       slOID,
				StopLossTriggerPx: slTrigger,
				CreatedAt:         time.Now().UTC(),
			})
			if err != nil {
				results = append(results, adoptHLPositionResult{
					StrategyID: entry.sc.ID,
					Symbol:     hlPos.Coin,
					Side:       side,
					Quantity:   qty,
					Action:     "adopt_failed",
					Note:       err.Error(),
				})
				continue
			}
			pendingInserted++
		}
		results = append(results, adoptHLPositionResult{
			StrategyID: entry.sc.ID,
			Symbol:     hlPos.Coin,
			Side:       side,
			Quantity:   qty,
			Action:     "adopted",
			Note:       note,
		})
	}

	// Drain newly inserted actions into in-memory state immediately.
	if pendingInserted > 0 {
		if err := ss.adoptPendingManualActionsSync(); err != nil {
			fmt.Fprintf(os.Stderr, "[adopt-hl-positions] drain failed: %v\n", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(adoptHLPositionsResponse{Results: results})
}
