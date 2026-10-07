package sampling

import "testing"

func TestDeterministicAndStable(t *testing.T) {
	d1 := NewDecider(0.5, 42)
	d2 := NewDecider(0.5, 42)
	var diverged int
	for i := 0; i < 1000; i++ {
		k := "session:u|s" + string(rune('a'+i%26)) + itoa(i/26)
		if d1.Keep(k) != d2.Keep(k) {
			diverged++
		}
	}
	if diverged != 0 {
		t.Fatal("same seed/rate must decide identically")
	}
	// Boundary rates.
	if NewDecider(1, 1).Keep("anything") != true {
		t.Fatal("rate 1 keeps all")
	}
	if NewDecider(0, 1).Keep("anything") != false {
		t.Fatal("rate 0 drops all")
	}
	// Rough ratio at 50%.
	d := NewDecider(0.5, 7)
	kept := 0
	for i := 0; i < 4000; i++ {
		if d.Keep(itoa(i)) {
			kept++
		}
	}
	ratio := float64(kept) / 4000
	if ratio < 0.45 || ratio > 0.55 {
		t.Fatalf("sampling ratio drifted: %f", ratio)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
