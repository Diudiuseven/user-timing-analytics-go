package dedup

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func TestDedupRetries(t *testing.T) {
	f := NewFilter(42, 1)
	e := model.Entry{ID: "e1", Kind: model.KindMark, Name: "a"}
	if !f.Accept(e) {
		t.Fatal("first delivery must be accepted")
	}
	for i := 0; i < 3; i++ {
		if f.Accept(e) {
			t.Fatal("retry must be rejected")
		}
	}
}

func TestSamplingDeterministic(t *testing.T) {
	entries := make([]model.Entry, 200)
	for i := range entries {
		entries[i] = model.Entry{ID: string(rune('a'+i%26)) + string(rune('A'+i/26)), Kind: model.KindFrame}
	}
	run := func(seed uint64, rate float64) []bool {
		f := NewFilter(seed, rate)
		out := make([]bool, len(entries))
		for i, e := range entries {
			out[i] = f.Accept(e)
		}
		return out
	}
	a := run(7, 0.5)
	b := run(7, 0.5)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("same seed must give same sampling")
		}
	}
	kept := 0
	for _, v := range a {
		if v {
			kept++
		}
	}
	if kept == 0 || kept == len(entries) {
		t.Fatalf("rate 0.5 should keep a fraction, kept %d", kept)
	}
	c := run(8, 0.5)
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
		}
	}
	if same {
		t.Fatal("different seed should (almost surely) differ")
	}
}

func TestSeenRoundTrip(t *testing.T) {
	f := NewFilter(1, 1)
	f.Accept(model.Entry{ID: "x"})
	f.Accept(model.Entry{ID: "y"})
	g := NewFilter(1, 1)
	g.LoadSeen(f.SeenKeys())
	if g.Accept(model.Entry{ID: "x"}) {
		t.Fatal("restored filter must reject seen key")
	}
	if !g.Accept(model.Entry{ID: "z"}) {
		t.Fatal("restored filter must accept new key")
	}
}
