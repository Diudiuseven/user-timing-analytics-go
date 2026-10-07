package measure

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func mk(name string, start int64, detail string) model.Event {
	return model.Event{Key: "k-" + name + "-" + itoa(start), Kind: model.KindMark, Name: name, Start: start, End: start, Detail: detail}
}
func mr(name string, start int64, detail string) model.Event {
	return model.Event{Key: "m-" + name, Kind: model.KindMeasure, Name: name, Start: start, End: start, Detail: detail}
}
func itoa(i int64) string {
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

func TestResolveStatuses(t *testing.T) {
	cfg := model.Config{MeasureMinDurMs: 1, MeasureMaxDurMs: 600000}
	events := []model.Event{
		mk("s1", 100, ""), mk("e1", 250, ""), mr("ok", 110, "s1,e1"),
		mr("noStart", 1, "missing,e1"),
		mk("onlyStart", 100, ""), mr("noEnd", 1, "onlyStart,noSuchEnd"),
		mk("bs", 300, ""), mk("be", 250, ""), mr("neg", 1, "bs,be"),
		mr("orphan", 1, ""),
	}
	res := Resolve(events, cfg)
	byName := map[string]model.MeasureResult{}
	for _, r := range res {
		byName[r.Name] = r
	}
	check := func(name string, st model.SegStatus) {
		t.Helper()
		r, ok := byName[name]
		if !ok {
			t.Fatalf("missing result %s", name)
		}
		if r.Status != st {
			t.Fatalf("%s = %s, want %s (%s)", name, r.Status, st, r.Reason)
		}
	}
	check("ok", model.SegComplete)
	if byName["ok"].DurMs != 150 {
		t.Fatalf("dur = %v want 150", byName["ok"].DurMs)
	}
	check("noStart", model.SegNoStart)
	check("noEnd", model.SegNoEnd)
	check("neg", model.SegBadDur)
	if byName["neg"].DurMs != 0 {
		t.Fatal("bad duration must not leak its raw value")
	}
	check("orphan", model.SegOrphan)
}
