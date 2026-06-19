package main

import "time"

const (
	strategyDrawdownFastIntervalSeconds = 90
	defaultDrawdownWarnThresholdPct     = 80
	defaultOpenPositionIntervalSeconds  = 120
)

// configuredDrawdownWarnThresholdPct returns the percent-of-max threshold at
// which a strategy enters per-strategy "drawdown warning" mode (and switches to
// the fast 90s check interval).
//
// Note: this currently reuses cfg.PortfolioRisk.WarnThresholdPct, which was
// originally designed for portfolio-level (PeakValue vs total equity) warnings.
// Per-strategy drawdown warning shares the same knob today; if the two ever
// need to diverge, add an optional StrategyConfig.DrawdownWarnThresholdPct
// override and prefer it over the portfolio-level value here.
func configuredDrawdownWarnThresholdPct(cfg *Config) float64 {
	if cfg != nil && cfg.PortfolioRisk != nil && cfg.PortfolioRisk.WarnThresholdPct > 0 {
		return cfg.PortfolioRisk.WarnThresholdPct
	}
	return defaultDrawdownWarnThresholdPct
}

func configuredStrategyIntervalSeconds(sc StrategyConfig, globalIntervalSeconds int) int {
	if sc.IntervalSeconds > 0 {
		return sc.IntervalSeconds
	}
	return globalIntervalSeconds
}

func configuredOpenPositionIntervalSeconds(sc StrategyConfig, globalOpenPositionIntervalSeconds int) int {
	if sc.OpenPositionIntervalSeconds > 0 {
		return sc.OpenPositionIntervalSeconds
	}
	if globalOpenPositionIntervalSeconds > 0 {
		return globalOpenPositionIntervalSeconds
	}
	return defaultOpenPositionIntervalSeconds
}

func strategyHasOpenPosition(s *StrategyState, symbol string) bool {
	if s == nil || symbol == "" {
		return false
	}
	pos := s.Positions[symbol]
	return pos != nil && pos.Quantity > 0
}

// strategyDrawdownWarningActive reports whether a strategy should switch to
// the fast check cadence because its current drawdown has crossed the warn
// threshold (warnThresholdPct % of MaxDrawdownPct). The check intentionally
// has no upper bound: if drawdown has exceeded MaxDrawdownPct but the circuit
// breaker hasn't flipped yet (CB is set inside CheckRisk during a real cycle),
// we still want the fast cadence so the next cycle can fire CB ASAP. The
// CircuitBreaker guard above handles the post-CB case.
func strategyDrawdownWarningActive(s *StrategyState, warnThresholdPct float64) bool {
	if s == nil || s.RiskState.CircuitBreaker {
		return false
	}
	r := s.RiskState
	if r.MaxDrawdownPct <= 0 || warnThresholdPct <= 0 {
		return false
	}
	warnDrawdownPct := r.MaxDrawdownPct * warnThresholdPct / 100
	return r.CurrentDrawdownPct > warnDrawdownPct
}

func effectiveStrategyIntervalSeconds(sc StrategyConfig, s *StrategyState, globalIntervalSeconds int, warnThresholdPct float64, globalOpenPositionIntervalSeconds int) int {
	interval := configuredStrategyIntervalSeconds(sc, globalIntervalSeconds)

	// Day-trading watch: accelerate checks while a position is open.
	if globalOpenPositionIntervalSeconds != 0 && strategyHasOpenPosition(s, sc.Symbol) {
		openInterval := configuredOpenPositionIntervalSeconds(sc, globalOpenPositionIntervalSeconds)
		if interval <= 0 || interval > openInterval {
			interval = openInterval
		}
	}

	if strategyDrawdownWarningActive(s, warnThresholdPct) && (interval <= 0 || interval > strategyDrawdownFastIntervalSeconds) {
		return strategyDrawdownFastIntervalSeconds
	}
	return interval
}

// effectiveStrategyIntervals computes the effective per-strategy check
// interval for every strategy in one pass. Callers that need the same
// intervals for both due-detection and the scheduler delay calculation should
// compute this map once per cycle (under mu.RLock) and pass it to both
// strategyIsDue and nextStrategyCheckDelay, instead of recomputing per call.
func effectiveStrategyIntervals(strategies []StrategyConfig, states map[string]*StrategyState, globalIntervalSeconds int, warnThresholdPct float64, globalOpenPositionIntervalSeconds int) map[string]int {
	out := make(map[string]int, len(strategies))
	for _, sc := range strategies {
		out[sc.ID] = effectiveStrategyIntervalSeconds(sc, states[sc.ID], globalIntervalSeconds, warnThresholdPct, globalOpenPositionIntervalSeconds)
	}
	return out
}

// nextStrategyCheckDelay returns the shortest wall-clock wait until any
// eligible strategy becomes due. Returns 0 when a strategy has never run or is
// already overdue. Returns -1 when no strategy is eligible (all skipped).
func nextStrategyCheckDelay(
	strategies []StrategyConfig,
	intervals map[string]int,
	lastRun map[string]time.Time,
	now time.Time,
) time.Duration {
	var minRemaining time.Duration = -1
	found := false

	for _, sc := range strategies {
		if shouldSkipZeroCapital(sc) {
			continue
		}
		interval := intervals[sc.ID]
		if interval <= 0 {
			continue
		}
		found = true

		last, exists := lastRun[sc.ID]
		if !exists {
			return 0
		}

		remaining := time.Duration(interval)*time.Second - now.Sub(last)
		if remaining <= 0 {
			return 0
		}
		if minRemaining < 0 || remaining < minRemaining {
			minRemaining = remaining
		}
	}

	if !found {
		return -1
	}
	return minRemaining
}

// schedulerDelay converts nextStrategyCheckDelay into a timer duration for the
// main loop. A zero delay yields 1s so the due-strategy pass runs promptly.
// When no strategy is eligible (-1), fall back to tick/global/60s intervals.
func schedulerDelay(
	strategies []StrategyConfig,
	intervals map[string]int,
	lastRun map[string]time.Time,
	globalIntervalSeconds int,
	now time.Time,
	fallbackSeconds int,
) time.Duration {
	delay := nextStrategyCheckDelay(strategies, intervals, lastRun, now)
	if delay == 0 {
		return time.Second
	}
	if delay < 0 {
		if fallbackSeconds > 0 {
			return time.Duration(fallbackSeconds) * time.Second
		}
		if globalIntervalSeconds > 0 {
			return time.Duration(globalIntervalSeconds) * time.Second
		}
		return 60 * time.Second
	}
	return delay
}
