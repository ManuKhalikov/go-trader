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

// canonicalStrategyPositionKey is the bare coin used as the Positions map key.
func canonicalStrategyPositionKey(sym string) string {
	return normalizeHlCoin(sym)
}

// lookupStrategyPosition finds a virtual position by bare coin, including legacy
// qualified keys (e.g. xyz:GOLD stored before canonicalization).
func lookupStrategyPosition(ss *StrategyState, sym string) (*Position, string) {
	if ss == nil || ss.Positions == nil {
		return nil, ""
	}
	bare := canonicalStrategyPositionKey(sym)
	if bare == "" {
		return nil, ""
	}
	if pos := ss.Positions[bare]; pos != nil {
		return pos, bare
	}
	for key, pos := range ss.Positions {
		if pos != nil && hlCoinsMatch(bare, key) {
			return pos, key
		}
	}
	return nil, ""
}

// setStrategyPosition stores pos under the bare coin and removes stale aliases.
func setStrategyPosition(ss *StrategyState, sym string, pos *Position) {
	if ss == nil || pos == nil {
		return
	}
	key := canonicalStrategyPositionKey(sym)
	pos.Symbol = key
	for existingKey := range ss.Positions {
		if hlCoinsMatch(existingKey, key) && existingKey != key {
			delete(ss.Positions, existingKey)
		}
	}
	ss.Positions[key] = pos
}

func deleteStrategyPosition(ss *StrategyState, sym string) {
	if ss == nil {
		return
	}
	if _, key := lookupStrategyPosition(ss, sym); key != "" {
		delete(ss.Positions, key)
	}
}

func hasStrategyPositionOpen(ss *StrategyState, sym string) bool {
	pos, _ := lookupStrategyPosition(ss, sym)
	return pos != nil && pos.Quantity > 0
}
