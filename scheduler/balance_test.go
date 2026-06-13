package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestResolveCapitalPct_NoCapitalPct(t *testing.T) {
	strategies := []StrategyConfig{
		{ID: "test-1", Capital: 1000, Platform: "binanceus"},
		{ID: "test-2", Capital: 2000, Platform: "hyperliquid"},
	}
	// Should be a no-op when no strategies have CapitalPct.
	resolveCapitalPct(strategies)
	if strategies[0].Capital != 1000 {
		t.Errorf("expected capital=1000, got %g", strategies[0].Capital)
	}
	if strategies[1].Capital != 2000 {
		t.Errorf("expected capital=2000, got %g", strategies[1].Capital)
	}
}

func TestResolveCapitalPct_FallbackCapital(t *testing.T) {
	// When balance fetch fails (unsupported platform without Python), should keep fallback capital.
	strategies := []StrategyConfig{
		{ID: "test-pct", Capital: 500, CapitalPct: 0.5, Platform: "binanceus"},
	}
	// binanceus doesn't have a Go-native balance fetch and check_balance.py won't work in tests,
	// so it should fall back to the existing capital value.
	resolveCapitalPct(strategies)
	if strategies[0].Capital != 500 {
		t.Errorf("expected fallback capital=500, got %g", strategies[0].Capital)
	}
}

func TestResolveCapitalPct_NoFallbackCapital(t *testing.T) {
	// When balance fetch fails and no fallback capital, should remain 0.
	strategies := []StrategyConfig{
		{ID: "test-no-fallback", Capital: 0, CapitalPct: 0.5, Platform: "binanceus"},
	}
	resolveCapitalPct(strategies)
	if strategies[0].Capital != 0 {
		t.Errorf("expected capital=0 (no fallback), got %g", strategies[0].Capital)
	}
}

func TestResolveCapitalPct_SharedWalletEachGetsFullBalance(t *testing.T) {
	origAddr := os.Getenv("HYPERLIQUID_ACCOUNT_ADDRESS")
	os.Setenv("HYPERLIQUID_ACCOUNT_ADDRESS", "0xshared")
	defer os.Setenv("HYPERLIQUID_ACCOUNT_ADDRESS", origAddr)

	perpsResp := map[string]interface{}{
		"marginSummary": map[string]string{
			"accountValue":    "800",
			"totalMarginUsed": "0",
		},
		"assetPositions": []interface{}{},
	}
	spotResp := map[string]interface{}{
		"balances": []map[string]string{
			{"coin": "USDC", "total": "0", "hold": "0"},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req["type"] == "spotClearinghouseState" {
			json.NewEncoder(w).Encode(spotResp)
			return
		}
		json.NewEncoder(w).Encode(perpsResp)
	}))
	defer ts.Close()

	origURL := hlMainnetURL
	hlMainnetURL = ts.URL
	defer func() { hlMainnetURL = origURL }()

	strategies := []StrategyConfig{
		{ID: "hl-a", Platform: "hyperliquid", Type: "perps", CapitalPct: 1.0, Args: []string{"adx_trend", "BTC", "4h", "--mode=live"}},
		{ID: "hl-b", Platform: "hyperliquid", Type: "perps", CapitalPct: 1.0, Args: []string{"adx_trend", "ETH", "4h", "--mode=live"}},
	}
	resolveCapitalPct(strategies)

	wantEach := 800.0
	if strategies[0].Capital != wantEach {
		t.Errorf("hl-a capital = %g, want %g (100%% of wallet)", strategies[0].Capital, wantEach)
	}
	if strategies[1].Capital != wantEach {
		t.Errorf("hl-b capital = %g, want %g (100%% of wallet)", strategies[1].Capital, wantEach)
	}
}
