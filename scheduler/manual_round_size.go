package main

import (
	"encoding/json"
	"fmt"
)

// HyperliquidRoundSizeResult is JSON from check_hyperliquid.py --round-size.
type HyperliquidRoundSizeResult struct {
	Symbol         string  `json:"symbol,omitempty"`
	RequestedSize  float64 `json:"requested_size,omitempty"`
	RoundedSize    float64 `json:"rounded_size,omitempty"`
	SzDecimals     int     `json:"sz_decimals,omitempty"`
	MinLot         float64 `json:"min_lot,omitempty"`
	MinNotionalUSD float64 `json:"min_notional_usd,omitempty"`
	RoundsToZero   bool    `json:"rounds_to_zero,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// RunHyperliquidRoundSize probes lot rounding for a coin qty before live execute.
func RunHyperliquidRoundSize(script, symbol string, size, markPrice float64) (*HyperliquidRoundSizeResult, string, error) {
	args := []string{
		"--round-size",
		fmt.Sprintf("--symbol=%s", symbol),
		fmt.Sprintf("--size=%g", size),
	}
	if markPrice > 0 {
		args = append(args, fmt.Sprintf("--mark-price=%g", markPrice))
	}
	stdout, stderr, err := RunPythonScript(script, args)
	return parseHyperliquidRoundSizeOutput(stdout, string(stderr), err)
}

func parseHyperliquidRoundSizeOutput(stdout []byte, stderrStr string, runErr error) (*HyperliquidRoundSizeResult, string, error) {
	var result HyperliquidRoundSizeResult
	if len(stdout) > 0 {
		if err := json.Unmarshal(stdout, &result); err != nil && runErr == nil {
			return nil, stderrStr, fmt.Errorf("parse round-size output: %w (stdout: %s)", err, string(stdout))
		}
	}
	// --round-size exits 1 when rounds_to_zero but still prints structured JSON.
	if result.RoundsToZero {
		return &result, stderrStr, nil
	}
	if result.Error != "" {
		return &result, stderrStr, fmt.Errorf("%s", result.Error)
	}
	if runErr != nil {
		return nil, stderrStr, fmt.Errorf("round-size error: %w (stderr: %s)", runErr, stderrStr)
	}
	return &result, stderrStr, nil
}

var runHyperliquidRoundSizeFn = RunHyperliquidRoundSize

func validateHLLotSize(script, symbol string, qty, mark float64) error {
	if script == "" || symbol == "" || qty <= 0 {
		return nil
	}
	result, stderr, err := runHyperliquidRoundSizeFn(script, symbol, qty, mark)
	if err != nil {
		msg := err.Error()
		if stderr != "" {
			msg = fmt.Sprintf("%s; stderr=%s", msg, stderr)
		}
		return fmt.Errorf("%s", msg)
	}
	if result == nil {
		return fmt.Errorf("nil round-size result for %s", symbol)
	}
	if result.RoundsToZero || result.RoundedSize <= 0 {
		msg := fmt.Sprintf(
			"size %g rounds to zero for %s (sz_decimals=%d, min_lot=%g)",
			qty, symbol, result.SzDecimals, result.MinLot,
		)
		if mark > 0 && result.MinNotionalUSD > 0 {
			msg += fmt.Sprintf("; minimum notional ~$%.2f at mark $%.2f", result.MinNotionalUSD, mark)
		} else if mark > 0 {
			msg += fmt.Sprintf("; at mark $%.2f need size >= %g", mark, result.MinLot)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
