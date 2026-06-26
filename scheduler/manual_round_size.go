package main

import (
	"encoding/json"
	"fmt"
	"math"
)

// hlExchangeMinNotionalUSD is HL's $10 exchange minimum plus a $1 buffer so
// lot rounding never pushes rounded_qty × mark below the floor (mirrors agent).
const hlExchangeMinNotionalUSD = 11.0

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

// ceilHLQtyToMinLot rounds qty up to the next lot step (sz_decimals).
func ceilHLQtyToMinLot(qty float64, szDecimals int) float64 {
	if qty <= 0 || szDecimals < 0 {
		return 0
	}
	step := math.Pow(10, -float64(szDecimals))
	return math.Ceil(qty/step-1e-12) * step
}

// ensureHLMinNotionalQty bumps qty so rounded_size × mark meets HL's exchange
// minimum. Returns the adjusted qty; logs via caller when bumped.
func ensureHLMinNotionalQty(script, symbol string, qty, mark float64) (float64, error) {
	if script == "" || symbol == "" || qty <= 0 || mark <= 0 {
		return qty, nil
	}
	result, _, err := runHyperliquidRoundSizeFn(script, symbol, qty, mark)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, fmt.Errorf("nil round-size result for %s", symbol)
	}
	rounded := result.RoundedSize
	if rounded > 0 && rounded*mark >= hlExchangeMinNotionalUSD {
		return qty, nil
	}
	// rounded == 0 (qty below min lot) OR rounded*mark below exchange minimum —
	// fall through to bump logic instead of erroring.
	minQty := hlExchangeMinNotionalUSD / mark
	bumped := ceilHLQtyToMinLot(minQty, result.SzDecimals)
	if bumped <= rounded {
		bumped = ceilHLQtyToMinLot(rounded+result.MinLot, result.SzDecimals)
	}
	if bumped*mark < hlExchangeMinNotionalUSD {
		return 0, fmt.Errorf(
			"cannot satisfy HL min notional $%.2f for %s at mark $%.2f (need qty >= %g)",
			hlExchangeMinNotionalUSD, symbol, mark, minQty,
		)
	}
	return bumped, nil
}
