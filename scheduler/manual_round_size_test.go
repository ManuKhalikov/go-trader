package main

import (
	"sync"
	"testing"
)

func TestCeilHLQtyToMinLot(t *testing.T) {
	got := ceilHLQtyToMinLot(0.00025, 5)
	want := 0.00025
	if got != want {
		t.Errorf("ceilHLQtyToMinLot(0.00025, 5) = %g, want %g", got, want)
	}
	got = ceilHLQtyToMinLot(0.00021, 5)
	want = 0.00022
	if got != want {
		t.Errorf("ceilHLQtyToMinLot(0.00021, 5) = %g, want %g", got, want)
	}
}

func TestEnsureHLMinNotionalQty_BumpsSubMinimum(t *testing.T) {
	orig := runHyperliquidRoundSizeFn
	defer func() { runHyperliquidRoundSizeFn = orig }()

	runHyperliquidRoundSizeFn = func(script, symbol string, size, markPrice float64) (*HyperliquidRoundSizeResult, string, error) {
		return &HyperliquidRoundSizeResult{
			Symbol:       symbol,
			RequestedSize: size,
			RoundedSize:  0.0002,
			SzDecimals:   5,
			MinLot:       0.00001,
		}, "", nil
	}

	// 0.0002 × $40000 = $8 — below HL $11 min.
	qty, err := ensureHLMinNotionalQty("script", "BTC", 0.0002, 40000)
	if err != nil {
		t.Fatalf("ensureHLMinNotionalQty: %v", err)
	}
	if qty*40000 < hlExchangeMinNotionalUSD {
		t.Fatalf("bumped qty %g still below min notional (~$%.2f)", qty, qty*40000)
	}
}

func TestEnsureHLMinNotionalQty_RoundsToZero(t *testing.T) {
	orig := runHyperliquidRoundSizeFn
	defer func() { runHyperliquidRoundSizeFn = orig }()

	// BTC at $58366 with qty=0.000188 — rounds to zero for sz_decimals=3 (min_lot=0.001).
	runHyperliquidRoundSizeFn = func(script, symbol string, size, markPrice float64) (*HyperliquidRoundSizeResult, string, error) {
		return &HyperliquidRoundSizeResult{
			Symbol:        symbol,
			RequestedSize: size,
			RoundedSize:   0, // rounds to zero
			RoundsToZero:  true,
			SzDecimals:    3,
			MinLot:        0.001,
			MinNotionalUSD: 58.37,
		}, "", nil
	}

	qty, err := ensureHLMinNotionalQty("script", "BTC", 0.0001884, 58366.50)
	if err != nil {
		t.Fatalf("ensureHLMinNotionalQty rounds-to-zero should bump, not error: %v", err)
	}
	if qty*58366.50 < hlExchangeMinNotionalUSD {
		t.Fatalf("bumped qty %g still below min notional (~$%.2f)", qty, qty*58366.50)
	}
	if qty < 0.001 {
		t.Fatalf("bumped qty %g is below min_lot 0.001", qty)
	}
}

func TestHLVirtualOnChainMismatched_FlatOnChain(t *testing.T) {
	sc := StrategyConfig{ID: "s1", Args: []string{"x", "ETH"}}
	ss := &StrategyState{
		Positions: map[string]*Position{
			"ETH": {Symbol: "ETH", Quantity: 0.5, Side: "long"},
		},
	}
	if !hlVirtualOnChainMismatched(sc, ss, nil) {
		t.Fatal("expected mismatch when virtual open and on-chain flat")
	}
}

func TestAppendHLReconcileMismatched_UnionsNonDue(t *testing.T) {
	state := &AppState{
		Strategies: map[string]*StrategyState{
			"s1": {
				ID: "s1",
				Positions: map[string]*Position{
					"ETH": {Symbol: "ETH", Quantity: 0.5, Side: "long"},
				},
			},
		},
	}
	all := []StrategyConfig{{ID: "s1", Args: []string{"x", "ETH"}}}
	var mu sync.RWMutex
	got := appendHLReconcileMismatched(nil, all, state, &mu, nil)
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("appendHLReconcileMismatched = %+v, want [s1]", got)
	}
}
