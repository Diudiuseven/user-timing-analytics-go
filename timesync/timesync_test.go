package timesync

import "testing"

func TestMedianOffsetOrderIndependent(t *testing.T) {
	samples := []int64{10, 5, 1000, 8, 12} // 1000 = one delayed batch
	want := EstimateOffset(samples)
	for i := 0; i < 20; i++ {
		// rotate samples to emulate arbitrary arrival order
		rot := append(append([]int64{}, samples[i%len(samples):]...), samples[:i%len(samples)]...)
		if got := EstimateOffset(rot); got != want {
			t.Fatalf("offset not order-independent: got %d want %d (rot %v)", got, want, rot)
		}
	}
	if want != 10 {
		t.Fatalf("median = %d, want 10 (delayed outlier ignored)", want)
	}
}

func TestMonotonicClampOnRollback(t *testing.T) {
	n := NewNormalizer(100, 0)
	in := []int64{1000, 1100, 900 /*rollback*/, 950, 1200}
	var got []int64
	var clamps int
	for _, c := range in {
		s, clamped, _ := n.Normalize(c)
		got = append(got, s)
		if clamped {
			clamps++
		}
	}
	want := []int64{1100, 1200, 1200, 1200, 1300}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %d want %d (all %v)", i, got[i], want[i], got)
		}
	}
	if clamps != 2 {
		t.Fatalf("clamps = %d, want 2", clamps)
	}
}

func TestJumpFlag(t *testing.T) {
	n := NewNormalizer(0, 1000)
	if _, _, j := n.Normalize(0); j {
		t.Fatal("first sample cannot be a jump")
	}
	if _, _, j := n.Normalize(500); j {
		t.Fatal("500ms move within threshold")
	}
	if _, _, j := n.Normalize(2_000_000); !j {
		t.Fatal("large forward jump must be flagged")
	}
	if s, clamped, _ := n.Normalize(100); !clamped || s != 2_000_000 {
		t.Fatalf("backwards after jump must clamp, got %d clamped %v", s, clamped)
	}
}
