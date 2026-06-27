package main

import "testing"

func TestNormalizeHlCoin(t *testing.T) {
	cases := map[string]string{
		"btc":           "BTC",
		"xyz:SP500":     "SP500",
		"xyz:GOLD-USD":  "GOLD",
		"BTC-USDC":      "BTC",
		"SPX":           "SP500",
	}
	for in, want := range cases {
		if got := normalizeHlCoin(in); got != want {
			t.Errorf("normalizeHlCoin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToHyperliquidCoin(t *testing.T) {
	cases := map[string]string{
		"BTC":   "BTC",
		"HYPE":  "HYPE",
		"SP500": "xyz:SP500",
		"GOLD":  "xyz:GOLD",
		"SPCX":  "xyz:SPCX",
	}
	for in, want := range cases {
		if got := toHyperliquidCoin(in); got != want {
			t.Errorf("toHyperliquidCoin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToHyperliquidCoinPreservesLowercaseDexPrefix(t *testing.T) {
	// http_manual used to ToUpper() the whole symbol, breaking HIP-3 (xyz:COIN).
	if got := toHyperliquidCoin("XYZ:SPCX"); got != "xyz:SPCX" {
		t.Errorf("toHyperliquidCoin(XYZ:SPCX) = %q, want xyz:SPCX", got)
	}
	if got := toHyperliquidCoin("xyz:SPCX"); got != "xyz:SPCX" {
		t.Errorf("toHyperliquidCoin(xyz:SPCX) = %q, want xyz:SPCX", got)
	}
}

func TestHlCoinsMatch(t *testing.T) {
	if !hlCoinsMatch("SP500", "xyz:SP500") {
		t.Error("expected SP500 and xyz:SP500 to match")
	}
	if hlCoinsMatch("BTC", "ETH") {
		t.Error("expected BTC and ETH not to match")
	}
}

func TestLookupStrategyPositionHIP3QualifiedLookup(t *testing.T) {
	ss := &StrategyState{
		Positions: map[string]*Position{
			"SP500": {
				Symbol:          "SP500",
				Quantity:        0.013,
				Side:            "long",
				OwnerStrategyID: "hl-roundtable-sp500",
				AvgCost:         7332.1,
			},
		},
	}
	pos, key := lookupStrategyPosition(ss, "xyz:SP500")
	if pos == nil {
		t.Fatal("expected position for xyz:SP500 lookup")
	}
	if key != "SP500" {
		t.Errorf("key = %q, want SP500", key)
	}
	if pos.Quantity != 0.013 {
		t.Errorf("quantity = %g, want 0.013", pos.Quantity)
	}
}
