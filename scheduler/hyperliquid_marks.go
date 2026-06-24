package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// fetchHyperliquidMids fetches the current mid prices for all HL perpetuals
// in a single authentication-free round-trip and returns a coin→price map
// filtered to the requested coins. Reuses hlMainnetURL from
// hyperliquid_balance.go so tests can redirect to a stub server.
//
// The /info allMids response is a flat JSON object: {"BTC":"67500.50", ...}.
// Each value is a numeric string. Coins not listed on HL are omitted from the
// returned map — the caller falls back to pos.AvgCost via the prices-miss
// path in PortfolioValue / PortfolioNotional (same graceful degradation as
// fetch_futures_marks.py misses).
//
// This is the correct oracle for HL perps positions; BinanceUS spot is wrong
// because spot/perps basis divergence (funding, liquidity, exchange-specific
// pricing) shows up as phantom PnL in PortfolioValue — fixes issue #263.
func fetchHyperliquidMids(coins []string) (map[string]float64, error) {
	if len(coins) == 0 {
		return map[string]float64{}, nil
	}

	var mainCoins, hip3Coins []string
	for _, c := range coins {
		bare := normalizeHlCoin(c)
		if hlHIP3Coins[bare] {
			hip3Coins = append(hip3Coins, bare)
		} else {
			mainCoins = append(mainCoins, bare)
		}
	}

	marks := make(map[string]float64, len(coins))

	if len(mainCoins) > 0 {
		mainMarks, err := fetchHyperliquidMidsPayload(map[string]string{"type": "allMids"}, mainCoins)
		if err != nil {
			return nil, err
		}
		for coin, px := range mainMarks {
			marks[coin] = px
		}
	}

	if len(hip3Coins) > 0 {
		hip3Marks, err := fetchHyperliquidMidsPayload(map[string]string{"type": "allMids", "dex": "xyz"}, hip3Coins)
		if err == nil {
			for coin, px := range hip3Marks {
				marks[coin] = px
			}
		}
		// HIP-3 equity assets may be absent from xyz allMids (or allMids may
		// fail); fall back to the last 1m candle close (same path as agent
		// manualTrade / minNotional).
		for _, bare := range hip3Coins {
			if marks[bare] > 0 {
				continue
			}
			px, err := fetchHyperliquidCandleClose(bare)
			if err != nil {
				continue
			}
			marks[bare] = px
		}
	}

	return marks, nil
}

// fetchHyperliquidCandleClose returns the most recent 1m candle close for a
// HIP-3 coin when allMids does not list it.
func fetchHyperliquidCandleClose(bareCoin string) (float64, error) {
	hlCoin := toHyperliquidCoin(bareCoin)
	endTime := time.Now().UnixMilli()
	startTime := endTime - 3*60*1000
	reqBody := map[string]any{
		"type": "candleSnapshot",
		"req": map[string]any{
			"coin":      hlCoin,
			"interval":  "1m",
			"startTime": startTime,
			"endTime":   endTime,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("marshal candleSnapshot request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(hlMainnetURL+"/info", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http %d from %s/info candleSnapshot", resp.StatusCode, hlMainnetURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read candleSnapshot response: %w", err)
	}

	var candles []struct {
		C json.RawMessage `json:"c"`
	}
	if err := json.Unmarshal(data, &candles); err != nil {
		return 0, fmt.Errorf("parse candleSnapshot response: %w", err)
	}
	if len(candles) == 0 {
		return 0, fmt.Errorf("candleSnapshot returned no candles for %s", hlCoin)
	}
	px, err := parseHlJSONPrice(candles[len(candles)-1].C)
	if err != nil || px <= 0 {
		return 0, fmt.Errorf("invalid candle close for %s", hlCoin)
	}
	return px, nil
}

// parseHlJSONPrice parses a Hyperliquid price field that may be a JSON string
// or number (HL candleSnapshot uses strings; some endpoints emit floats).
func parseHlJSONPrice(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("empty price")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseFloat(s, 64)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, nil
	}
	return 0, fmt.Errorf("unrecognized price format")
}

func fetchHyperliquidMidsPayload(payload map[string]string, coins []string) (map[string]float64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal allMids request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(hlMainnetURL+"/info", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d from %s/info allMids", resp.StatusCode, hlMainnetURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read allMids response: %w", err)
	}

	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse allMids response: %w", err)
	}

	want := make(map[string]bool, len(coins))
	for _, c := range coins {
		want[normalizeHlCoin(c)] = true
	}

	marks := make(map[string]float64, len(coins))
	for coin, priceStr := range raw {
		bare := normalizeHlCoin(coin)
		if !want[bare] {
			continue
		}
		p, err := strconv.ParseFloat(priceStr, 64)
		if err != nil || p <= 0 {
			continue
		}
		marks[bare] = p
	}
	return marks, nil
}
