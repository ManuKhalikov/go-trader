package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestComputeFallbackATR(t *testing.T) {
	cases := []struct {
		fillPrice float64
		wantATR   float64
		wantOK    bool
	}{
		{2500, 12.5, true},  // default 0.5%: 2500 * 0.5/100 = 12.5
		{100, 0.5, true},    // default 0.5%: 100 * 0.5/100 = 0.5
		{0, 0, false},       // fillPrice == 0
		{-1, 0, false},      // fillPrice < 0
	}
	for _, c := range cases {
		got, ok := computeFallbackATR(c.fillPrice)
		if ok != c.wantOK {
			t.Errorf("computeFallbackATR(%.2f): ok=%v want %v", c.fillPrice, ok, c.wantOK)
		}
		if ok && (got < c.wantATR*0.9999 || got > c.wantATR*1.0001) {
			t.Errorf("computeFallbackATR(%.2f): atr=%.6f want %.6f", c.fillPrice, got, c.wantATR)
		}
	}
}

func TestComputeFallbackATR_EnvPct(t *testing.T) {
	t.Setenv("MANUAL_OPEN_ATR_FALLBACK_PCT", "2.0")
	got, ok := computeFallbackATR(1000)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// 1000 * 2.0 / 100 = 20.0
	if got < 19.999 || got > 20.001 {
		t.Errorf("got %.6f, want 20.0", got)
	}
}

func TestPlaceManualProtectionInline_NoTiers(t *testing.T) {
	sc := StrategyConfig{
		Type:          "manual",
		Platform:      "hyperliquid",
		CloseStrategy: &StrategyRef{Name: "tp_at_pct"}, // not tiered_tp_atr*
	}
	oids, warn, err := placeManualProtectionInline(sc, "long", 0.8, 2500, 12.5, 1.0, 0, nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if warn != "" {
		t.Errorf("expected empty warn, got %q", warn)
	}
	if len(oids) != 0 {
		t.Errorf("expected no OIDs for non-tiered strategy, got %v", oids)
	}
}

func TestPlaceManualProtectionInline_SkipsSLSyncWhenAlreadyArmed(t *testing.T) {
	orig := runHLSyncProtectionFn
	defer func() { runHLSyncProtectionFn = orig }()

	var gotSLMult float64
	runHLSyncProtectionFn = func(script, symbol, side string, size, avgCost, entryATR, stopLossATRMult float64, tiers []hlProtectionTier, stopLossOID int64, tpOIDs []int64, _ []bool, _ bool, _ []bool, _ []int64, _ []byte) (*HyperliquidProtectionSyncResult, string, error) {
		gotSLMult = stopLossATRMult
		return &HyperliquidProtectionSyncResult{TPOIDs: []int64{1001}}, "", nil
	}

	sc := StrategyConfig{
		Type:          "manual",
		Platform:      "hyperliquid",
		CloseStrategy: &StrategyRef{Name: "tiered_tp_atr_live"},
	}
	_, _, err := placeManualProtectionInline(sc, "long", 0.002, 63854, 949, 2.0, 12345, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSLMult != 0 {
		t.Errorf("stopLossATRMult for sync-protection: got %.2f want 0 when SL OID already set", gotSLMult)
	}
}

func TestPlaceManualProtectionInline_PassesSLSyncWhenNotYetArmed(t *testing.T) {
	orig := runHLSyncProtectionFn
	defer func() { runHLSyncProtectionFn = orig }()

	var gotSLMult float64
	runHLSyncProtectionFn = func(script, symbol, side string, size, avgCost, entryATR, stopLossATRMult float64, tiers []hlProtectionTier, stopLossOID int64, tpOIDs []int64, _ []bool, _ bool, _ []bool, _ []int64, _ []byte) (*HyperliquidProtectionSyncResult, string, error) {
		gotSLMult = stopLossATRMult
		return &HyperliquidProtectionSyncResult{TPOIDs: []int64{1001}}, "", nil
	}

	sc := StrategyConfig{
		Type:          "manual",
		Platform:      "hyperliquid",
		CloseStrategy: &StrategyRef{Name: "tiered_tp_atr_live"},
	}
	_, _, err := placeManualProtectionInline(sc, "long", 0.002, 63854, 949, 2.0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSLMult != 2.0 {
		t.Errorf("stopLossATRMult for sync-protection: got %.2f want 2.0 when SL not yet armed", gotSLMult)
	}
}

func TestPlaceManualProtectionInline_TPErrorsSurface(t *testing.T) {
	orig := runHLSyncProtectionFn
	defer func() { runHLSyncProtectionFn = orig }()

	runHLSyncProtectionFn = func(script, symbol, side string, size, avgCost, entryATR, stopLossATRMult float64, tiers []hlProtectionTier, stopLossOID int64, tpOIDs []int64, _ []bool, _ bool, _ []bool, _ []int64, _ []byte) (*HyperliquidProtectionSyncResult, string, error) {
		return &HyperliquidProtectionSyncResult{
			TPOIDs:   []int64{1001, 1002},
			TPErrors: []string{"TP1: rejected", ""},
		}, "", nil
	}

	sc := StrategyConfig{
		Type:          "manual",
		Platform:      "hyperliquid",
		Script:        "shared_scripts/check_hyperliquid.py",
		Symbol:        "ETH",
		CloseStrategy: &StrategyRef{Name: "tiered_tp_atr_live"},
	}
	oids, warn, err := placeManualProtectionInline(sc, "long", 0.8, 2500, 12.5, 1.0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(warn, "TP1") {
		t.Errorf("expected warn to mention TP1, got %q", warn)
	}
	if len(oids) != 2 {
		t.Errorf("expected 2 OIDs, got %v", oids)
	}
}

func TestWarnNotifier_NilNotifier(t *testing.T) {
	// Should not panic when notifier is nil.
	warnNotifier(nil, "test warning")
}

// TestAttemptManualOpenCleanup_Success covers the happy path: queue insert
// failed, cleanup close succeeded, SL+TPs cancelled in one shot.
func TestAttemptManualOpenCleanup_Success(t *testing.T) {
	orig := manualOpenCleanupCloseFn
	defer func() { manualOpenCleanupCloseFn = orig }()

	var gotSymbol string
	var gotSize *float64
	var gotCancelOIDs []int64
	manualOpenCleanupCloseFn = func(symbol string, partialSz *float64, cancelOIDs []int64) (*HyperliquidCloseResult, string, error) {
		gotSymbol = symbol
		gotSize = partialSz
		gotCancelOIDs = cancelOIDs
		return &HyperliquidCloseResult{}, "", nil
	}

	cleanedUp, msg := attemptManualOpenCleanup("ETH", 0.8, 12345, []int64{67890, 67891})
	if !cleanedUp {
		t.Fatalf("expected cleanedUp=true, got msg=%q", msg)
	}
	if gotSymbol != "ETH" {
		t.Errorf("symbol: got %q want ETH", gotSymbol)
	}
	if gotSize == nil || *gotSize != 0.8 {
		t.Errorf("partialSz: got %v want 0.8", gotSize)
	}
	if len(gotCancelOIDs) != 3 || gotCancelOIDs[0] != 12345 || gotCancelOIDs[1] != 67890 || gotCancelOIDs[2] != 67891 {
		t.Errorf("cancelOIDs: got %v want [12345 67890 67891]", gotCancelOIDs)
	}
}

// TestAttemptManualOpenCleanup_CloseFails covers the worst-case path where the
// recovery close itself fails — operator must be alerted that intervention is
// required because both fill ownership and cleanup failed.
func TestAttemptManualOpenCleanup_CloseFails(t *testing.T) {
	orig := manualOpenCleanupCloseFn
	defer func() { manualOpenCleanupCloseFn = orig }()

	manualOpenCleanupCloseFn = func(symbol string, partialSz *float64, cancelOIDs []int64) (*HyperliquidCloseResult, string, error) {
		return nil, "stderr noise", fmt.Errorf("rpc timeout")
	}

	cleanedUp, msg := attemptManualOpenCleanup("ETH", 0.8, 12345, []int64{67890})
	if cleanedUp {
		t.Fatalf("expected cleanedUp=false, got msg=%q", msg)
	}
	if !strings.Contains(msg, "rpc timeout") {
		t.Errorf("msg should mention close failure cause; got %q", msg)
	}
}

// TestAttemptManualOpenCleanup_CancelStopLossError: position closed but the
// inline trigger cancel reported an error — partial success: position is
// flat (good) but some triggers may persist (operator should verify).
func TestAttemptManualOpenCleanup_CancelStopLossError(t *testing.T) {
	orig := manualOpenCleanupCloseFn
	defer func() { manualOpenCleanupCloseFn = orig }()

	manualOpenCleanupCloseFn = func(symbol string, partialSz *float64, cancelOIDs []int64) (*HyperliquidCloseResult, string, error) {
		return &HyperliquidCloseResult{CancelStopLossError: "trigger 12345 not found"}, "", nil
	}

	cleanedUp, msg := attemptManualOpenCleanup("ETH", 0.8, 12345, nil)
	if !cleanedUp {
		t.Fatalf("expected cleanedUp=true (position closed), got msg=%q", msg)
	}
	if !strings.Contains(msg, "trigger 12345 not found") {
		t.Errorf("msg should surface cancel error; got %q", msg)
	}
}

// TestAttemptManualOpenCleanup_FiltersZeroOIDs: zero/unset OIDs must not be
// passed to the cancel list — close_hyperliquid_position.py would otherwise
// reject the request.
func TestAttemptManualOpenCleanup_FiltersZeroOIDs(t *testing.T) {
	orig := manualOpenCleanupCloseFn
	defer func() { manualOpenCleanupCloseFn = orig }()

	var gotCancelOIDs []int64
	manualOpenCleanupCloseFn = func(symbol string, partialSz *float64, cancelOIDs []int64) (*HyperliquidCloseResult, string, error) {
		gotCancelOIDs = cancelOIDs
		return &HyperliquidCloseResult{}, "", nil
	}

	// SL OID = 0 (not placed) and one TP OID = 0 (placement failed for one tier).
	attemptManualOpenCleanup("ETH", 0.8, 0, []int64{0, 67891})
	if len(gotCancelOIDs) != 1 || gotCancelOIDs[0] != 67891 {
		t.Errorf("cancelOIDs should filter zeros; got %v want [67891]", gotCancelOIDs)
	}
}

// TestAttemptManualOpenCleanup_NilResultNoError: a broken stub returning
// (nil, "", nil) must not report false success — guarded by the nil check.
func TestAttemptManualOpenCleanup_NilResultNoError(t *testing.T) {
	orig := manualOpenCleanupCloseFn
	defer func() { manualOpenCleanupCloseFn = orig }()

	manualOpenCleanupCloseFn = func(symbol string, partialSz *float64, cancelOIDs []int64) (*HyperliquidCloseResult, string, error) {
		return nil, "", nil
	}

	cleanedUp, msg := attemptManualOpenCleanup("ETH", 0.8, 12345, nil)
	if cleanedUp {
		t.Fatalf("expected cleanedUp=false for nil result, got msg=%q", msg)
	}
	if !strings.Contains(msg, "nil") {
		t.Errorf("msg should mention nil result; got %q", msg)
	}
}

func TestParseManualTPTiersJSON_DeltaFractions(t *testing.T) {
	raw := `[{"atr_mult":1.5,"fraction":0.4},{"atr_mult":3.5,"fraction":0.35},{"atr_mult":5.0,"fraction":0.25}]`
	tiers := parseManualTPTiersJSON(raw)
	if len(tiers) != 3 {
		t.Fatalf("tiers len = %d, want 3", len(tiers))
	}
	want := []float64{0.4, 0.75, 1.0}
	for i, frac := range want {
		if tiers[i].Fraction != frac {
			t.Errorf("tier[%d].Fraction = %g, want %g", i, tiers[i].Fraction, frac)
		}
	}
}

func TestNormalizeProtectionTierFractions_CumulativePassthrough(t *testing.T) {
	in := []hlProtectionTier{
		{Multiple: 1.5, Fraction: 0.4},
		{Multiple: 3.5, Fraction: 0.75},
		{Multiple: 5.0, Fraction: 1.0},
	}
	out := normalizeProtectionTierFractions(in)
	if len(out) != 3 || out[2].Fraction != 1.0 {
		t.Fatalf("normalize cumulative passthrough: got %+v", out)
	}
}
