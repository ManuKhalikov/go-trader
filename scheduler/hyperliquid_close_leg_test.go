package main

import "testing"

func TestHyperliquidResultIsCloseLeg(t *testing.T) {
	s := &StrategyState{
		Positions: map[string]*Position{
			"ETH": {Symbol: "ETH", Quantity: 1.0, Side: "long"},
		},
	}
	closeResult := &HyperliquidResult{Symbol: "ETH", Signal: -1}
	if !hyperliquidResultIsCloseLeg(closeResult, s) {
		t.Fatal("expected signal=-1 long position to be a close leg")
	}
	openResult := &HyperliquidResult{Symbol: "ETH", Signal: 1}
	if hyperliquidResultIsCloseLeg(openResult, s) {
		t.Fatal("signal=1 against long position is not a close leg")
	}
	flat := &StrategyState{Positions: map[string]*Position{}}
	if hyperliquidResultIsCloseLeg(closeResult, flat) {
		t.Fatal("close signal without virtual position is not a close leg")
	}
}
