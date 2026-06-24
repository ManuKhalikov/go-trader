package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type manualOpenBody struct {
	StrategyID        string  `json:"strategy_id"`
	Side              string  `json:"side"`
	Notional          float64 `json:"notional"`
	Symbol            string  `json:"symbol"`
	FillPrice         float64 `json:"fill_price,omitempty"`
	SignalID          string  `json:"signal_id,omitempty"`
	ClientOrderID     string  `json:"client_order_id,omitempty"`
	ExpiresAt         string  `json:"expires_at,omitempty"`
	StopLossATRMult   float64 `json:"stop_loss_atr_mult,omitempty"`
	StopLossTriggerPx float64 `json:"stop_loss_trigger_px,omitempty"`
	TPTiers           string  `json:"tp_tiers,omitempty"`
}

type manualCloseBody struct {
	StrategyID string `json:"strategy_id"`
	Symbol     string `json:"symbol,omitempty"`
}

func (ss *StatusServer) configPathForManual() string {
	if ss.configPath != "" {
		return ss.configPath
	}
	if p := os.Getenv("GOTRADER_CONFIG"); p != "" {
		return p
	}
	return "scheduler/config.json"
}

// summarizeManualStderr returns the last actionable stderr line, skipping audit
// prefixes like "[manual-open] signal_id=..." that obscure the real failure.
func summarizeManualStderr(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	var last string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[manual-open] signal_id=") {
			continue
		}
		last = line
	}
	return last
}

func manualErrorDetail(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	summary := summarizeManualStderr(stderr)
	const maxLen = 2048
	if summary != "" && summary != stderr {
		combined := summary + "\n" + stderr
		if len(combined) > maxLen {
			return combined[:maxLen]
		}
		return combined
	}
	if len(stderr) > maxLen {
		return stderr[:maxLen]
	}
	return stderr
}

// statusBindHost returns the listen host for the status HTTP server.
// STATUS_BIND_ALL=true binds 0.0.0.0 for Railway private networking.
func statusBindHost() string {
	if strings.EqualFold(os.Getenv("STATUS_BIND_ALL"), "true") {
		return "0.0.0.0"
	}
	return "localhost"
}

func (ss *StatusServer) requireManualAuth(w http.ResponseWriter, r *http.Request) bool {
	if ss.statusToken != "" {
		if r.Header.Get("Authorization") != "Bearer "+ss.statusToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return false
		}
	}
	return true
}

func (ss *StatusServer) handleManualOpenHTTP(w http.ResponseWriter, r *http.Request) {
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

	var body manualOpenBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}

	body.StrategyID = strings.TrimSpace(body.StrategyID)
	body.Side = strings.ToLower(strings.TrimSpace(body.Side))
	body.Symbol = toHyperliquidCoin(strings.TrimSpace(body.Symbol))

	if body.StrategyID == "" || body.Side == "" || body.Notional <= 0 || body.Symbol == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "strategy_id, side, notional, and symbol are required"})
		return
	}
	if body.Side != "long" && body.Side != "short" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "side must be long or short"})
		return
	}

	if body.ClientOrderID != "" && ss.isDuplicateManualOpen(body.ClientOrderID) {
		_ = ss.adoptPendingManualActionsSync()
		if ss.hasAdoptedManualPosition(body.StrategyID, body.Symbol, body.Side) {
			json.NewEncoder(w).Encode(map[string]string{
				"status":      "ok",
				"duplicate":   "true",
				"signal_id":   body.SignalID,
				"strategy_id": body.StrategyID,
			})
			return
		}
		// Stale idempotency from a prior attempt that never adopted a position.
		ss.clearManualOpenSuccess(body.ClientOrderID)
	}

	args := []string{
		body.StrategyID,
		"--side", body.Side,
		"--notional", strconv.FormatFloat(body.Notional, 'f', -1, 64),
		"--symbol", body.Symbol,
		"--config", ss.configPathForManual(),
	}
	if body.FillPrice > 0 {
		args = append(args, "--fill-price", strconv.FormatFloat(body.FillPrice, 'f', -1, 64))
	}
	if body.SignalID != "" {
		args = append(args, "--signal-id", body.SignalID)
	}
	if body.ClientOrderID != "" {
		args = append(args, "--client-order-id", body.ClientOrderID)
	}
	if body.StopLossATRMult > 0 {
		args = append(args, "--stop-loss-atr-mult", strconv.FormatFloat(body.StopLossATRMult, 'f', -1, 64))
	}
	if body.StopLossTriggerPx > 0 {
		args = append(args, "--stop-loss-trigger-px", strconv.FormatFloat(body.StopLossTriggerPx, 'f', -1, 64))
	}
	if strings.TrimSpace(body.TPTiers) != "" {
		args = append(args, "--tp-tiers", body.TPTiers)
	}

	code, stderr := runManualOpenCapture(args)
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		resp := map[string]any{
			"error":     "manual-open failed",
			"exit_code": code,
		}
		if stderr != "" {
			resp["detail"] = manualErrorDetail(stderr)
		}
		json.NewEncoder(w).Encode(resp)
		return
	}
	// CLI exit 0 means HL fill was confirmed and the action was queued — record
	// idempotency now so agent retries cannot place a second on-chain order.
	if body.ClientOrderID != "" {
		ss.recordManualOpenSuccess(body.ClientOrderID)
	}
	if err := ss.adoptPendingManualActionsSync(); err != nil {
		fmt.Fprintf(os.Stderr, "[manual-open] state adoption failed (HL fill ok): %v\n", err)
	} else if !ss.hasAdoptedManualPosition(body.StrategyID, body.Symbol, body.Side) {
		fmt.Fprintf(os.Stderr,
			"[manual-open] warning: HL fill queued but position not adopted for %s %s %s — scheduler will retry drain\n",
			body.StrategyID, body.Side, body.Symbol)
	}
	json.NewEncoder(w).Encode(map[string]string{
		"status":      "ok",
		"strategy_id": body.StrategyID,
		"symbol":      body.Symbol,
		"signal_id":   body.SignalID,
	})
}

func (ss *StatusServer) isDuplicateManualOpen(clientOrderID string) bool {
	ss.manualOpenMu.Lock()
	defer ss.manualOpenMu.Unlock()
	if ss.manualOpenIds == nil {
		return false
	}
	return ss.manualOpenIds[clientOrderID]
}

func (ss *StatusServer) recordManualOpenSuccess(clientOrderID string) {
	if strings.TrimSpace(clientOrderID) == "" {
		return
	}
	ss.manualOpenMu.Lock()
	defer ss.manualOpenMu.Unlock()
	if ss.manualOpenIds == nil {
		ss.manualOpenIds = make(map[string]bool)
	}
	ss.manualOpenIds[clientOrderID] = true
}

func (ss *StatusServer) clearManualOpenSuccess(clientOrderID string) {
	if strings.TrimSpace(clientOrderID) == "" {
		return
	}
	ss.manualOpenMu.Lock()
	defer ss.manualOpenMu.Unlock()
	if ss.manualOpenIds != nil {
		delete(ss.manualOpenIds, clientOrderID)
	}
}

func (ss *StatusServer) configForManualSync() *Config {
	ss.strategiesMu.RLock()
	strategies := append([]StrategyConfig(nil), ss.strategies...)
	ss.strategiesMu.RUnlock()
	return &Config{Strategies: strategies}
}

// adoptPendingManualActionsSync drains pending_manual_actions into in-memory
// state so /status reflects manual fills before the HTTP response returns.
func (ss *StatusServer) adoptPendingManualActionsSync() error {
	if ss == nil || ss.state == nil || ss.stateDB == nil || ss.mu == nil {
		return fmt.Errorf("status server not initialized for manual drain")
	}
	cfg := ss.configForManualSync()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	drainPendingManualActions(ss.state, cfg, ss.stateDB)
	return SaveStateWithDB(ss.state, cfg, ss.stateDB)
}

func (ss *StatusServer) hasAdoptedManualPosition(strategyID, symbol, side string) bool {
	if ss == nil || ss.state == nil || ss.mu == nil {
		return false
	}
	strategyID = strings.TrimSpace(strategyID)
	symbol = normalizeHlCoin(strings.TrimSpace(symbol))
	side = strings.ToLower(strings.TrimSpace(side))
	if strategyID == "" || symbol == "" || (side != "long" && side != "short") {
		return false
	}

	ss.mu.RLock()
	defer ss.mu.RUnlock()
	strat := ss.state.Strategies[strategyID]
	if strat == nil {
		return false
	}
	for symKey, pos := range strat.Positions {
		if pos == nil || pos.Quantity <= 0 {
			continue
		}
		posSym := normalizeHlCoin(pos.Symbol)
		if posSym == "" {
			posSym = normalizeHlCoin(symKey)
		}
		if posSym == symbol && pos.Side == side {
			return true
		}
	}
	return false
}

func (ss *StatusServer) handleEmergencyCloseHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ss.requireManualAuth(w, r) {
		return
	}
	var body manualCloseBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	args := []string{"--config", ss.configPathForManual()}
	if sym := toHyperliquidCoin(strings.TrimSpace(body.Symbol)); sym != "" {
		args = append(args, "--symbol", sym)
	}
	code, stderr := runManualCloseCapture(append([]string{"hl-roundtable"}, args...))
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error": "emergency-close failed", "detail": strings.TrimSpace(stderr)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (ss *StatusServer) handleManualCloseHTTP(w http.ResponseWriter, r *http.Request) {
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

	var body manualCloseBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}

	body.StrategyID = strings.TrimSpace(body.StrategyID)
	if body.StrategyID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "strategy_id is required"})
		return
	}

	args := []string{body.StrategyID, "--config", ss.configPathForManual()}
	if sym := toHyperliquidCoin(strings.TrimSpace(body.Symbol)); sym != "" {
		args = append(args, "--symbol", sym)
	}
	code, stderr := runManualCloseCapture(args)
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		resp := map[string]any{
			"error":     "manual-close failed",
			"exit_code": code,
		}
		if stderr != "" {
			resp["detail"] = strings.TrimSpace(stderr)
		}
		json.NewEncoder(w).Encode(resp)
		return
	}
	if err := ss.adoptPendingManualActionsSync(); err != nil {
		fmt.Fprintf(os.Stderr, "[manual-close] state adoption failed: %v\n", err)
	}
	json.NewEncoder(w).Encode(map[string]string{
		"status":      "ok",
		"strategy_id": body.StrategyID,
	})
}

func runManualOpenCapture(args []string) (int, string) {
	return captureStderr(func() int { return runManualOpen(args) })
}

func runManualCloseCapture(args []string) (int, string) {
	return captureStderr(func() int { return runManualClose(args) })
}

func captureStderr(fn func() int) (int, string) {
	r, w, err := os.Pipe()
	if err != nil {
		return fn(), ""
	}
	old := os.Stderr
	os.Stderr = w
	code := fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return code, buf.String()
}
