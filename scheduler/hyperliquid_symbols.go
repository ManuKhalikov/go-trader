package main

import "strings"

// hlHIP3Coins lists builder-dex (HIP-3) perps that trade on the xyz dex.
// Config and TRADE_PAIRS use bare names (SP500); Hyperliquid API calls need
// the qualified form (xyz:SP500). Main-dex coins (BTC, ETH, SOL, HYPE) are
// unchanged.
var hlHIP3Coins = map[string]bool{
	"SP500": true,
	"GOLD":  true,
	"NVDA":  true,
	"TSLA":  true,
	"SPCX":  true,
}

// normalizeHlCoin strips dex prefixes and pair suffixes to the bare ticker used
// in strategy config (e.g. xyz:SP500 → SP500, BTC-USDC → BTC).
func normalizeHlCoin(coin string) string {
	c := strings.ToUpper(strings.TrimSpace(coin))
	if idx := strings.LastIndex(c, ":"); idx >= 0 {
		c = c[idx+1:]
	}
	if slash := strings.Index(c, "/"); slash >= 0 {
		c = c[:slash]
	}
	if strings.HasSuffix(c, "-USDC") {
		c = strings.TrimSuffix(c, "-USDC")
	}
	if strings.HasSuffix(c, "-USD") {
		c = strings.TrimSuffix(c, "-USD")
	}
	switch c {
	case "SPX":
		return "SP500"
	}
	return c
}

// toHyperliquidCoin returns the coin name Hyperliquid /info and SDK calls expect.
func toHyperliquidCoin(coin string) string {
	bare := normalizeHlCoin(coin)
	if hlHIP3Coins[bare] {
		return "xyz:" + bare
	}
	return bare
}

// hlCoinsMatch compares a configured strategy symbol with an on-chain coin field.
func hlCoinsMatch(configured, onChain string) bool {
	return normalizeHlCoin(configured) == normalizeHlCoin(onChain)
}
