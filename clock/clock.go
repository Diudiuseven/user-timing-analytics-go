// Package clock 负责客户端时钟偏移校正。
//
// 客户端时钟与服务端时钟存在偏移，且客户端时钟可能跳变（NTP 校准、
// 用户改时间等）。Corrector 依据批次级时间对（客户端发送时刻、服务端
// 接收时刻）估计偏移：
//
//   - 传输延迟只会增大表观偏移，因此迄今最小的表观偏移最接近真实偏移；
//   - 表观偏移向上跳变超过可容忍传输延迟阈值时，判定为客户端时钟跳变，
//     采用新偏移并记录修正次数（可追溯），保证映射后的观测时间轴不倒流；
//   - 正常迟到批次（表观偏移与当前估计一致）不触发修正，其条目仍映射到
//     真实服务端时间，由上层按迟到数据路径处理。
package clock

import "github.com/Diudiuseven/user-timing-analytics-go/model"

// Corrector 维护客户端时钟到服务端时钟的偏移估计。
type Corrector struct {
	offset        int64
	hasOffset     bool
	jumpThreshold int64 // 可容忍的最大传输延迟（表观偏移向上波动上限）
	// Adjustments 记录发生的时钟跳变修正次数（可追溯）。
	Adjustments int
}

// NewCorrector 创建校正器。jumpThreshold 为可容忍的最大传输延迟（ms）。
func NewCorrector(jumpThreshold int64) *Corrector {
	if jumpThreshold < 0 {
		jumpThreshold = 0
	}
	return &Corrector{jumpThreshold: jumpThreshold}
}

// ObserveBatch 用批次级时间对（客户端发送时刻、服务端接收时刻）更新偏移估计。
func (c *Corrector) ObserveBatch(clientSentAt, serverReceivedAt int64) {
	if clientSentAt <= 0 || serverReceivedAt <= 0 {
		return
	}
	off := serverReceivedAt - clientSentAt
	switch {
	case !c.hasOffset:
		c.offset = off
		c.hasOffset = true
	case off < c.offset:
		// 更小的表观偏移更接近真实偏移（或客户端时钟向前跳变）。
		c.offset = off
	case off > c.offset+c.jumpThreshold:
		// 表观偏移向上跳变超过可容忍传输延迟：判定客户端时钟回拨，
		// 采用新偏移，保证后续条目映射后的时间轴不倒流。
		c.offset = off
		c.Adjustments++
	}
}

// ToServer 把客户端时间转换为服务端时间。
func (c *Corrector) ToServer(clientTime int64) int64 {
	if !c.hasOffset {
		return clientTime
	}
	return clientTime + c.offset
}

// CorrectedEntry 是时钟校正后的条目。
type CorrectedEntry struct {
	Entry      model.Entry
	ServerTime int64
}
