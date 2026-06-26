#!/usr/bin/env python3
"""Fetch open perps positions and open orders for the configured HL wallet.

Read-only; used by go-trader /adopt-hl-positions endpoint to reconcile
on-chain positions with internal state.

Outputs JSON to stdout:
{
  "positions": [
    {"coin": "HYPE", "size": -0.18, "entry_price": 61.625, "unrealized_pnl": -0.12}
  ],
  "open_orders": [
    {"coin": "HYPE", "oid": 12345, "side": "B", "sz": 0.18,
     "is_trigger": true, "trigger_px": 64.42, "reduce_only": true,
     "order_type": "Stop Market", "trigger_condition": "above 64.42"}
  ]
}
"""
import sys
import os
import json

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..', 'platforms', 'hyperliquid'))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..', 'shared_tools'))


def main():
    try:
        from adapter import HyperliquidExchangeAdapter
        adapter = HyperliquidExchangeAdapter()

        positions = adapter.get_open_positions()

        open_orders = []
        account_address = adapter._account_address
        if account_address:
            try:
                raw_orders = adapter._info.open_orders(account_address) or []
                for o in raw_orders:
                    if not isinstance(o, dict):
                        continue
                    trigger_px_raw = o.get("triggerPx")
                    open_orders.append({
                        "coin": o.get("coin", ""),
                        "oid": int(o.get("oid", 0)),
                        "side": o.get("side", ""),
                        "sz": float(o.get("sz", 0) or 0),
                        "is_trigger": bool(o.get("isTrigger", False)),
                        "trigger_px": float(trigger_px_raw) if trigger_px_raw else 0.0,
                        "trigger_condition": o.get("triggerCondition", ""),
                        "order_type": o.get("orderType", ""),
                        "reduce_only": bool(o.get("reduceOnly", False)),
                    })
            except Exception as e:
                sys.stderr.write(f"[fetch_hl_open_positions] open_orders failed: {e}\n")

        print(json.dumps({"positions": positions, "open_orders": open_orders}))
        sys.exit(0)
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)


if __name__ == "__main__":
    main()
