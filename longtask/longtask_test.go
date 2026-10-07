package longtask

import "testing"

func TestMergeOverlaps(t *testing.T) {
	iv := []Interval{{100, 200}, {150, 250}, {240, 300}, {500, 600}}
	m := Merge(iv, 50)
	if len(m) != 2 {
		t.Fatalf("merged = %v, want 2 unioned intervals", m)
	}
	if m[0].Start != 100 || m[0].End != 300 {
		t.Fatalf("first union = %v, want [100,300)", m[0])
	}
	// duplicate interval must be absorbed
	m2 := Merge([]Interval{{100, 200}, {100, 200}}, 50)
	if len(m2) != 1 || m2[0].End-m2[0].Start != 100 {
		t.Fatalf("duplicate long task must not double-count: %v", m2)
	}
}

func TestAttribution(t *testing.T) {
	merged := Merge([]Interval{{100, 300}}, 50)
	wins := []Window{{ID: "ixn:tap", Start: 120, End: 200}}
	stats := Attribute(merged, wins, "nav:n1")
	if len(stats) != 1 {
		t.Fatalf("stats = %v", stats)
	}
	var ixn, nav float64
	for _, a := range stats[0].Attributions {
		switch a.WindowID {
		case "ixn:tap":
			ixn += a.Duration
		case "nav:n1":
			nav += a.Duration
		}
	}
	if ixn != 80 || nav != 120 {
		t.Fatalf("ixn=%v nav=%v, want 80/120", ixn, nav)
	}
	if ixn+nav != stats[0].Duration {
		t.Fatal("attribution shares must sum to unioned duration")
	}
}
