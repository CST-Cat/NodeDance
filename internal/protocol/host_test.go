package protocol

import "testing"

func TestNodeAssetLineTypeValidation(t *testing.T) {
	for _, ok := range []string{"", LineTypeKVM, LineTypeNAT, LineTypeHomelab} {
		if err := ValidateNodeAsset(NodeAsset{Currency: "USD", BillingCycleDays: 30, ExpiryNoticeDays: 7, TrafficAccounting: "sum", TrafficWarningPercent: 90, LineType: ok}); err != nil {
			t.Fatalf("line type %q rejected: %v", ok, err)
		}
	}
	if err := ValidateNodeAsset(NodeAsset{Currency: "USD", BillingCycleDays: 30, ExpiryNoticeDays: 7, TrafficAccounting: "sum", TrafficWarningPercent: 90, LineType: "fiber"}); err == nil {
		t.Fatal("unknown line type accepted")
	}
	if !ValidNodeLineType("") || ValidNodeLineType("lxc") {
		t.Fatal("line type predicate mismatch")
	}
}
