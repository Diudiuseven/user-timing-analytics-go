package store

import (
	"path/filepath"
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func TestFileJournalAndSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenFile(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	batches := []model.Batch{
		{BatchID: "b1", UserID: "u", SessionID: "s", PageID: "p", ServerTimeMs: 1,
			Entries: []model.Entry{{Kind: model.KindMark, Name: "x", StartMs: 9}}},
		{BatchID: "b2", UserID: "u", SessionID: "s", PageID: "p", ServerTimeMs: 2},
	}
	for _, b := range batches {
		if err := st.AppendBatch(b); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListBatches()
	if err != nil || len(got) != 2 || got[0].BatchID != "b1" {
		t.Fatalf("journal round-trip failed: %v %+v", err, got)
	}

	s := NewState()
	s.SeenBatches["u|b|b1"] = true
	s.ClockDeltas["s"] = []int64{10, 20}
	if err := st.SaveState(s); err != nil {
		t.Fatal(err)
	}
	// Reopen and recover exactly.
	st2, err := OpenFile(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := st2.ListBatches()
	if len(got2) != 2 {
		t.Fatalf("journal must survive reopen: %d", len(got2))
	}
	snap, ok, err := st2.LoadState()
	if err != nil || !ok || !snap.SeenBatches["u|b|b1"] || len(snap.ClockDeltas["s"]) != 2 {
		t.Fatalf("snapshot round-trip failed: ok=%v err=%v snap=%+v", ok, err, snap)
	}
}
