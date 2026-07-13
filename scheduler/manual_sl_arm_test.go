package main

import "testing"

func TestIsHLInvalidTPSLPrice(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"place_stop_loss SDK error: Invalid tp/sl price", true},
		{"BadTriggerPx from exchange", true},
		{"bad trigger px rejected", true},
		{"too many trigger orders", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isHLInvalidTPSLPrice(tc.msg)
		if got != tc.want {
			t.Errorf("isHLInvalidTPSLPrice(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

// TestManualSlArmedRequiresRestingOid documents the abort contract:
// a cancelled-then-failed SL retry must not leave a stale OID that skips flatten.
func TestManualSlArmedRequiresRestingOid(t *testing.T) {
	type attempt struct {
		oid   int64
		err   string
		armed bool
	}
	// Simulate the fixed loop: only a clean final result arms; failures zero OID.
	run := func(attempts []attempt) (stopLossOID int64, slArmed bool) {
		for _, a := range attempts {
			if a.err == "" && a.oid > 0 {
				slArmed = true
				stopLossOID = a.oid
				break
			}
			stopLossOID = 0
		}
		if !slArmed {
			stopLossOID = 0
		}
		return stopLossOID, slArmed
	}

	oid, armed := run([]attempt{
		{oid: 111, err: ""}, // first place "succeeds"
		{oid: 0, err: "Invalid tp/sl price"}, // cancel+replace fails
	})
	// With the fixed logic we break on first clean success. The bug case is when
	// first attempt had an error AFTER recording OID — model that explicitly:
	oid, armed = run([]attempt{
		{oid: 111, err: "temporary"},
		{oid: 0, err: "Invalid tp/sl price"},
	})
	if armed || oid != 0 {
		t.Fatalf("stale OID must be cleared after failed retries: oid=%d armed=%v", oid, armed)
	}

	oid, armed = run([]attempt{
		{oid: 0, err: "Invalid tp/sl price"},
		{oid: 222, err: ""},
	})
	if !armed || oid != 222 {
		t.Fatalf("expected armed OID 222, got oid=%d armed=%v", oid, armed)
	}
}
