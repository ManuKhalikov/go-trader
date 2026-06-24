package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseHyperliquidRoundSizeOutputRoundsToZero(t *testing.T) {
	stdout := []byte(`{"symbol":"BTC","requested_size":0.00025,"rounded_size":0,"sz_decimals":5,"min_lot":0.00001,"min_notional_usd":1,"rounds_to_zero":true}`)
	result, _, err := parseHyperliquidRoundSizeOutput(stdout, "", fmt.Errorf("exit status 1"))
	if err != nil {
		t.Fatalf("expected nil err for rounds_to_zero payload, got: %v", err)
	}
	if result == nil || !result.RoundsToZero {
		t.Fatalf("expected rounds_to_zero result, got %+v", result)
	}
	if err := lotSizeErrorFromRoundResult(result, 0.00025, "BTC", 100000); err == nil {
		t.Fatal("lotSizeErrorFromRoundResult should fail when rounds_to_zero")
	}
}

func lotSizeErrorFromRoundResult(result *HyperliquidRoundSizeResult, qty float64, symbol string, mark float64) error {
	if result.RoundsToZero || result.RoundedSize <= 0 {
		msg := fmt.Sprintf(
			"size %g rounds to zero for %s (sz_decimals=%d, min_lot=%g)",
			qty, symbol, result.SzDecimals, result.MinLot,
		)
		if mark > 0 && result.MinNotionalUSD > 0 {
			msg += fmt.Sprintf("; minimum notional ~$%.2f at mark $%.2f", result.MinNotionalUSD, mark)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func TestSummarizeManualStderr(t *testing.T) {
	stderr := "[manual-open] signal_id=sig_1 client_order_id=sig_1\nerror: strategy \"hl-roundtable-hype\" has type=\"perps\"; manual-open/close only works with type=manual strategies\n"
	got := summarizeManualStderr(stderr)
	want := `error: strategy "hl-roundtable-hype" has type="perps"; manual-open/close only works with type=manual strategies`
	if got != want {
		t.Fatalf("summarizeManualStderr = %q, want %q", got, want)
	}
	detail := manualErrorDetail(stderr)
	if !strings.Contains(detail, want) {
		t.Fatalf("manualErrorDetail should include actionable error, got %q", detail)
	}
}

func TestConfigPathForManualPrefersServerConfig(t *testing.T) {
	t.Setenv("GOTRADER_CONFIG", "scheduler/config.json")
	ss := &StatusServer{configPath: "scheduler/config.railway.json"}
	if got := ss.configPathForManual(); got != "scheduler/config.railway.json" {
		t.Fatalf("configPathForManual() = %q, want server config path", got)
	}
}

func TestManualOpenIdempotencyOnlyAfterSuccess(t *testing.T) {
	ss := &StatusServer{}

	if ss.isDuplicateManualOpen("sig_test") {
		t.Fatal("fresh id should not be duplicate")
	}
	ss.recordManualOpenSuccess("sig_test")
	if !ss.isDuplicateManualOpen("sig_test") {
		t.Fatal("recorded success should be duplicate")
	}
	ss.clearManualOpenSuccess("sig_test")
	if ss.isDuplicateManualOpen("sig_test") {
		t.Fatal("cleared id should not be duplicate")
	}

	ss2 := &StatusServer{}
	ss2.recordManualOpenSuccess("")
	if ss2.manualOpenIds != nil {
		t.Fatal("empty client_order_id must not be recorded")
	}
}
