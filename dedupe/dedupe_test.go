package dedupe

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func TestBatchAndEntryDedupe(t *testing.T) {
	s := NewSet()
	if !s.Add(BatchKey("u1", "b1")) {
		t.Fatal("first add must succeed")
	}
	if s.Add(BatchKey("u1", "b1")) {
		t.Fatal("retry batch must dedupe")
	}
	if !s.Add(BatchKey("u2", "b1")) {
		t.Fatal("same batch id for different user is distinct")
	}
	e := model.Entry{Kind: model.KindMark, Name: "x", StartMs: 5}
	k1 := EntryKey("b1", 0, e)
	k2 := EntryKey("b1", 0, e)
	if k1 != k2 {
		t.Fatal("identical entries must have equal keys")
	}
	if !s.Add(k1) || s.Add(k2) {
		t.Fatal("identical entries must collapse")
	}
	if !s.Add(EntryKey("b1", 1, e)) {
		t.Fatal("same payload at different in-batch position is a distinct entry")
	}
}
