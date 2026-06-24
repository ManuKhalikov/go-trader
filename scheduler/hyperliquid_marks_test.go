package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchHyperliquidMids_Basic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		if req["type"] != "allMids" {
			http.Error(w, "wrong type", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"BTC":  "67500.50",
			"ETH":  "3200.10",
			"HYPE": "12.50",
			"SOL":  "150.00",
		})
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	marks, err := fetchHyperliquidMids([]string{"BTC", "ETH"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(marks["BTC"]-67500.50) > 1e-6 {
		t.Errorf("BTC = %v, want 67500.50", marks["BTC"])
	}
	if math.Abs(marks["ETH"]-3200.10) > 1e-6 {
		t.Errorf("ETH = %v, want 3200.10", marks["ETH"])
	}
	// SOL not requested — must be absent.
	if _, ok := marks["SOL"]; ok {
		t.Errorf("SOL should not be in returned marks (not requested)")
	}
	if len(marks) != 2 {
		t.Errorf("len(marks) = %d, want 2", len(marks))
	}
}

func TestFetchHyperliquidMids_EmptyCoins(t *testing.T) {
	// No coins requested — must return empty without hitting the network.
	marks, err := fetchHyperliquidMids(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(marks) != 0 {
		t.Errorf("expected empty map, got %v", marks)
	}
}

func TestFetchHyperliquidMids_CoinMissing(t *testing.T) {
	// A requested coin absent from the allMids response should simply be
	// absent from the returned map — caller falls back to pos.AvgCost.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"BTC": "67500.50"}) //nolint:errcheck
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	marks, err := fetchHyperliquidMids([]string{"BTC", "PURR"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(marks["BTC"]-67500.50) > 1e-6 {
		t.Errorf("BTC = %v, want 67500.50", marks["BTC"])
	}
	if _, ok := marks["PURR"]; ok {
		t.Errorf("PURR should be absent (not in allMids response)")
	}
}

func TestFetchHyperliquidMids_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	_, err := fetchHyperliquidMids([]string{"BTC"})
	if err == nil {
		t.Error("expected error on HTTP 500, got nil")
	}
}

func TestFetchHyperliquidMids_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-json")) //nolint:errcheck
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	_, err := fetchHyperliquidMids([]string{"BTC"})
	if err == nil {
		t.Error("expected error on invalid JSON, got nil")
	}
}

func TestFetchHyperliquidMids_ZeroPriceOmitted(t *testing.T) {
	// A coin with a zero or invalid price string must be omitted from the
	// returned map — same skip-zero semantics as mergeFuturesMarks.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"BTC": "67500.50",
			"ETH": "0",
			"SOL": "bad",
		})
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	marks, err := fetchHyperliquidMids([]string{"BTC", "ETH", "SOL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(marks["BTC"]-67500.50) > 1e-6 {
		t.Errorf("BTC = %v, want 67500.50", marks["BTC"])
	}
	if _, ok := marks["ETH"]; ok {
		t.Errorf("ETH should be omitted (price=0)")
	}
	if _, ok := marks["SOL"]; ok {
		t.Errorf("SOL should be omitted (invalid price string)")
	}
}

func TestFetchHyperliquidMids_HIP3Qualified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Type string `json:"type"`
			Dex  string `json:"dex"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		if req.Type != "allMids" || req.Dex != "xyz" {
			http.Error(w, "expected xyz allMids", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"xyz:SP500": "7355.70"}) //nolint:errcheck
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	marks, err := fetchHyperliquidMids([]string{"xyz:SP500"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(marks["SP500"]-7355.70) > 1e-6 {
		t.Errorf("SP500 = %v, want 7355.70", marks["SP500"])
	}
}

func TestFetchHyperliquidMids_HIP3CandleFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}
		var req map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		var reqType string
		json.Unmarshal(req["type"], &reqType) //nolint:errcheck
		switch reqType {
		case "allMids":
			json.NewEncoder(w).Encode(map[string]string{}) //nolint:errcheck
		case "candleSnapshot":
			json.NewEncoder(w).Encode([]map[string]string{{"c": "7355.70"}}) //nolint:errcheck
		default:
			http.Error(w, "unexpected request type", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	marks, err := fetchHyperliquidMids([]string{"SP500"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(marks["SP500"]-7355.70) > 1e-6 {
		t.Errorf("SP500 = %v, want 7355.70 from candleSnapshot fallback", marks["SP500"])
	}
}

func TestFetchHyperliquidMids_HIP3CandleFallbackOnAllMidsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}
		var req map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		var reqType string
		json.Unmarshal(req["type"], &reqType) //nolint:errcheck
		if reqType == "allMids" {
			http.Error(w, "allMids unavailable", http.StatusInternalServerError)
			return
		}
		if reqType == "candleSnapshot" {
			json.NewEncoder(w).Encode([]map[string]float64{{"c": 7355.70}}) //nolint:errcheck
			return
		}
		http.Error(w, "unexpected request type", http.StatusBadRequest)
	}))
	defer srv.Close()

	orig := hlMainnetURL
	hlMainnetURL = srv.URL
	defer func() { hlMainnetURL = orig }()

	marks, err := fetchHyperliquidMids([]string{"SP500"})
	if err != nil {
		t.Fatalf("expected candle fallback after allMids error, got: %v", err)
	}
	if math.Abs(marks["SP500"]-7355.70) > 1e-6 {
		t.Errorf("SP500 = %v, want 7355.70 from candleSnapshot after allMids error", marks["SP500"])
	}
}

func TestParseHlJSONPrice(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
	}{
		{`"7355.70"`, 7355.70},
		{`7355.70`, 7355.70},
	}
	for _, c := range cases {
		px, err := parseHlJSONPrice(json.RawMessage(c.raw))
		if err != nil {
			t.Fatalf("parseHlJSONPrice(%s): %v", c.raw, err)
		}
		if math.Abs(px-c.want) > 1e-6 {
			t.Errorf("parseHlJSONPrice(%s) = %v, want %v", c.raw, px, c.want)
		}
	}
}
