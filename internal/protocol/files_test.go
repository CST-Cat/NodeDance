package protocol

import "testing"

func TestFileBandwidthUsesDecimalMegabytes(t *testing.T) {
	for _, value := range []float64{0, 0.001024, 0.004096, 1, 12.5} {
		if !ValidFileBandwidth(value) {
			t.Fatalf("valid MB/s rejected: %v", value)
		}
	}
	for _, value := range []float64{-1, 0.000001, float64(MaxFileSize)/1000000 + 1} {
		if ValidFileBandwidth(value) {
			t.Fatalf("invalid MB/s accepted: %v", value)
		}
	}
	if FileBandwidthBytes(12.5) != 12500000 || FileBandwidthBytes(0.004096) != 4096 {
		t.Fatal("MB/s limiter conversion differs")
	}
}
