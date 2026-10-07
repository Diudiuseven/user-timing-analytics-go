// Package session 负责会话与导航切分，把条目归属到观测单元。
//
// 切分规则（对同一用户按服务端时间有序处理，结果与到达顺序无关）：
//   - navigate：开启新观测单元（PageID/NavID 取自事件，段号归零）；
//   - suspend / unload：关闭当前单元；
//   - resume / bfcache_restore：同页面同导航下段号 +1，前后两段指标不串联；
//   - 普通条目：归属当前打开单元；若无打开单元则隐式开启，
//     若 PageID/NavID 与当前单元不同则视为隐式导航并切分。
package session

import (
	"github.com/Diudiuseven/user-timing-analytics-go/clock"
	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// Splitter 按用户维护观测单元切分状态。
type Splitter struct {
	states map[string]*userState // key: userID
}

type userState struct {
	current model.UnitKey
	open    bool
}

// NewSplitter 创建切分器。
func NewSplitter() *Splitter { return &Splitter{states: make(map[string]*userState)} }

// Reset 清空全部状态（用于全量重算）。
func (s *Splitter) Reset() { s.states = make(map[string]*userState) }

// Assign 将一条已校正条目归属到观测单元，必要时切分新单元。
// 调用方必须保证同一用户的条目按 ServerTime 单调不减送达。
func (s *Splitter) Assign(userID string, e clock.CorrectedEntry) model.UnitKey {
	st, ok := s.states[userID]
	if !ok {
		st = &userState{current: model.UnitKey{UserID: userID}}
		s.states[userID] = st
	}
	ent := e.Entry
	if ent.Kind == model.KindLifecycle {
		s.applyLifecycle(st, ent)
		return st.current
	}
	// 普通条目。
	if !st.open {
		// 无打开单元：隐式开启。若之前有上下文（如挂起后未收到
		// resume 就出现新条目）则视为恢复，段号 +1；否则段号从 0 开始。
		seg := 0
		if st.current.PageID != "" || st.current.NavID != "" {
			seg = st.current.Segment + 1
		}
		st.current = model.UnitKey{
			UserID:  userID,
			PageID:  ent.PageID,
			NavID:   ent.NavID,
			Segment: seg,
		}
		st.open = true
		return st.current
	}
	if ent.PageID != "" && (ent.PageID != st.current.PageID || ent.NavID != st.current.NavID) {
		// 页面或导航标识变化：隐式导航切分。
		st.current = model.UnitKey{UserID: userID, PageID: ent.PageID, NavID: ent.NavID, Segment: 0}
	}
	return st.current
}

func (s *Splitter) applyLifecycle(st *userState, ent model.Entry) {
	switch ent.Name {
	case model.LcNavigate:
		st.current = model.UnitKey{UserID: st.current.UserID, PageID: ent.PageID, NavID: ent.NavID, Segment: 0}
		st.open = true
	case model.LcSuspend, model.LcUnload:
		st.open = false
	case model.LcResume, model.LcBFCacheRestore:
		// 恢复即新段：不得把挂起前后的指标串在一起。
		st.current.Segment++
		if ent.PageID != "" {
			st.current.PageID = ent.PageID
		}
		if ent.NavID != "" {
			st.current.NavID = ent.NavID
		}
		st.open = true
	}
}
