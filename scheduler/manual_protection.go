package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// runHLSyncProtectionFn is a package var so tests can stub without spawning Python.
var runHLSyncProtectionFn = RunHyperliquidSyncProtection

// formatProtectionSyncWarnings extracts per-field error strings from a
// HyperliquidProtectionSyncResult into a flat slice. Prefers the tiered
// TPErrors slice; falls back to legacy TP1Error/TP2Error scalar fields.
func formatProtectionSyncWarnings(result *HyperliquidProtectionSyncResult) []string {
	var warns []string
	if result.StopLossError != "" {
		warns = append(warns, "SL: "+result.StopLossError)
	}
	for i, e := range result.TPErrors {
		if e != "" {
			warns = append(warns, fmt.Sprintf("TP%d: %s", i+1, e))
		}
	}
	if len(result.TPErrors) == 0 {
		if result.TP1Error != "" {
			warns = append(warns, "TP1: "+result.TP1Error)
		}
		if result.TP2Error != "" {
			warns = append(warns, "TP2: "+result.TP2Error)
		}
	}
	for _, oid := range result.TPCancelFailedOIDs {
		if oid > 0 {
			warns = append(warns, fmt.Sprintf("surplus TP cancel OID=%d failed (will retry)", oid))
		}
	}
	for _, oid := range result.TPCancelFilledOIDs {
		if oid > 0 {
			warns = append(warns, fmt.Sprintf("surplus TP OID=%d filled on-chain (reconciler)", oid))
		}
	}
	return warns
}

// computeFallbackATR returns a leverage-aware ATR fallback of 0.1*fillPrice/leverage,
// representing a price move that risks 10% of deployed margin at 1× ATR. Returns
// ok=false when leverage or fillPrice are not positive (caller must warn naked).
func computeFallbackATR(fillPrice, leverage float64) (float64, bool) {
	if leverage <= 0 || fillPrice <= 0 {
		return 0, false
	}
	return 0.1 * fillPrice / leverage, true
}

// placeManualProtectionInline calls --sync-protection inline after a manual fill
// and returns the placed TP OIDs. Returns (nil, "", nil) when no tiers are configured
// without spawning Python. A non-empty warnMsg signals a partial failure (position
// remains open; caller should warn but not abort).
// tpTierOverride, when non-empty, replaces strategyTPTiers(sc) for this open only.
func placeManualProtectionInline(
	sc StrategyConfig,
	side string,
	fillQty, fillPrice, entryATR, effectiveSLATRMult float64,
	stopLossOID int64,
	tpTierOverride []hlProtectionTier,
) ([]int64, string, error) {
	tiers := tpTierOverride
	if len(tiers) == 0 {
		tiers = strategyTPTiers(sc)
	}
	if len(tiers) == 0 {
		return nil, "", nil
	}

	// SL is armed inline in manual-open before this call. Passing the ATR mult
	// into sync-protection would re-run the SL path; if HL's open-order indexer
	// has not yet listed the freshly placed OID, sync-protection treats it as
	// cancelled and places a duplicate stop (#manual-open duplicate SL).
	slMultForSync := effectiveSLATRMult
	if stopLossOID > 0 {
		slMultForSync = 0
	}

	result, stderr, err := runHLSyncProtectionFn(
		sc.Script, sc.Symbol, side, fillQty, fillPrice, entryATR,
		slMultForSync, tiers, stopLossOID, nil, nil, false, nil, nil, nil,
	)
	if stderr != "" {
		fmt.Fprintf(os.Stderr, "[manual-open] sync-protection stderr: %s\n", stderr)
	}
	if err != nil {
		return nil, "", err
	}
	if result == nil {
		return nil, "nil result from protection sync", nil
	}
	if result.Error != "" {
		return nil, result.Error, nil
	}

	return result.TPOIDs, strings.Join(formatProtectionSyncWarnings(result), "; "), nil
}

type manualTPTierJSON struct {
	ATRMult    float64 `json:"atr_mult"`
	ATRMultiple float64 `json:"atr_multiple"`
	ATRMultCamel float64 `json:"atrMult"`
	Fraction   float64 `json:"fraction"`
	CloseFraction float64 `json:"close_fraction"`
}

// parseManualTPTiersJSON parses Roundtable agent TP tier overrides from JSON.
func parseManualTPTiersJSON(raw string) []hlProtectionTier {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var items []manualTPTierJSON
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		fmt.Fprintf(os.Stderr, "[manual-open] invalid --tp-tiers JSON: %v\n", err)
		return nil
	}
	tiers := make([]hlProtectionTier, 0, len(items))
	for _, item := range items {
		mult := item.ATRMult
		if mult <= 0 {
			mult = item.ATRMultiple
		}
		if mult <= 0 {
			mult = item.ATRMultCamel
		}
		frac := item.Fraction
		if frac <= 0 {
			frac = item.CloseFraction
		}
		if mult <= 0 || frac <= 0 {
			continue
		}
		tiers = append(tiers, hlProtectionTier{Multiple: mult, Fraction: frac})
	}
	if len(tiers) < 1 {
		fmt.Fprintf(os.Stderr, "[manual-open] --tp-tiers parsed zero valid tiers\n")
		return nil
	}
	return finalizeProtectionTiers(tiers)
}

// manualOpenCleanupCloseFn is the close path used by attemptManualOpenCleanup.
// Exposed as a package var so tests can stub without spawning Python (#634).
var manualOpenCleanupCloseFn = func(symbol string, partialSz *float64, cancelOIDs []int64) (*HyperliquidCloseResult, string, error) {
	return RunHyperliquidClose(hyperliquidLiveCloseScript, symbol, partialSz, cancelOIDs)
}

// attemptManualOpenCleanup tries to flatten a position that was just opened by
// manual-open and cancel its protective triggers, after a fatal error
// (typically pending-action queue insert failure) prevented the scheduler from
// adopting the position. Without this, the next reconcile cycle would see an
// unowned on-chain position with orphaned reduce-only SL/TP orders (#634).
//
// Sized to fillQty (not the full on-chain position) so a peer manual/perps
// position on the same coin is preserved. Returns (cleanedUp, msg) where msg
// is suitable for inclusion in an operator notification.
func attemptManualOpenCleanup(symbol string, fillQty float64, stopLossOID int64, tpOIDs []int64) (bool, string) {
	cancelOIDs := make([]int64, 0, 1+len(tpOIDs))
	if stopLossOID > 0 {
		cancelOIDs = append(cancelOIDs, stopLossOID)
	}
	for _, oid := range tpOIDs {
		if oid > 0 {
			cancelOIDs = append(cancelOIDs, oid)
		}
	}

	sz := fillQty
	result, stderr, err := manualOpenCleanupCloseFn(symbol, &sz, cancelOIDs)
	if stderr != "" {
		fmt.Fprintf(os.Stderr, "[manual-open cleanup] close stderr: %s\n", stderr)
	}
	if err != nil {
		return false, fmt.Sprintf("close failed: %v", err)
	}
	if result == nil {
		return false, "cleanup close returned nil result"
	}
	if result.CancelStopLossError != "" {
		return true, fmt.Sprintf("position closed but trigger cancel reported: %s", result.CancelStopLossError)
	}
	return true, "position flattened and orphan triggers cancelled"
}

// warnNotifier writes msg to stderr and, when the notifier has backends, also
// broadcasts to all channels and fires an owner DM.
func warnNotifier(notifier *MultiNotifier, msg string) {
	fmt.Fprintln(os.Stderr, "[WARN] "+msg)
	if notifier != nil && notifier.HasBackends() {
		notifier.SendToAllChannels(msg)
		notifier.SendOwnerDM(msg)
	}
}
