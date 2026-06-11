package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type manualOpenBody struct {
	StrategyID string  `json:"strategy_id"`
	Side       string  `json:"side"`
	Notional   float64 `json:"notional"`
	Symbol     string  `json:"symbol"`
	FillPrice  float64 `json:"fill_price,omitempty"`
}

type manualCloseBody struct {
	StrategyID string `json:"strategy_id"`
}

func manualConfigPath() string {
	if p := os.Getenv("GOTRADER_CONFIG"); p != "" {
		return p
	}
	return "scheduler/config.json"
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
	body.Symbol = strings.ToUpper(strings.TrimSpace(body.Symbol))

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

	args := []string{
		body.StrategyID,
		"--side", body.Side,
		"--notional", strconv.FormatFloat(body.Notional, 'f', -1, 64),
		"--symbol", body.Symbol,
		"--config", manualConfigPath(),
	}
	if body.FillPrice > 0 {
		args = append(args, "--fill-price", strconv.FormatFloat(body.FillPrice, 'f', -1, 64))
	}

	code := runManualOpen(args)
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"error":     "manual-open failed",
			"exit_code": code,
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{
		"status":      "ok",
		"strategy_id": body.StrategyID,
		"symbol":      body.Symbol,
	})
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

	args := []string{body.StrategyID, "--config", manualConfigPath()}
	code := runManualClose(args)
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"error":     "manual-close failed",
			"exit_code": code,
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{
		"status":      "ok",
		"strategy_id": body.StrategyID,
	})
}
