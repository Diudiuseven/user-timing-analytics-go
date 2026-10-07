package clock

import "testing"

func TestOffsetEstimation(t *testing.T) {
	c := NewCorrector(1000)
	c.ObserveBatch(1000, 1500) // offset 500
	c.ObserveBatch(2000, 2600) // 表观 600，在阈值内波动，不采用也不计跳变
	if c.Adjustments != 0 {
		t.Fatalf("in-threshold jitter must not count as jump, got %d", c.Adjustments)
	}
	c.ObserveBatch(3000, 3400) // 表观 400，更小，采用
	if got := c.ToServer(4000); got != 4400 {
		t.Fatalf("want 4400, got %d", got)
	}
}

func TestClockRollbackDetected(t *testing.T) {
	c := NewCorrector(300)
	c.ObserveBatch(1000, 1500) // offset 500
	if got := c.ToServer(900); got != 1400 {
		t.Fatalf("want 1400, got %d", got)
	}
	// 客户端时钟回拨：表观偏移跳变到 1100，超过阈值
	c.ObserveBatch(500, 1600)
	if c.Adjustments != 1 {
		t.Fatalf("want 1 adjustment, got %d", c.Adjustments)
	}
	// 采用新偏移：回拨后的条目映射不倒流
	if got := c.ToServer(200); got != 1300 {
		t.Fatalf("want 1300, got %d", got)
	}
	if got := c.ToServer(950); got != 2050 {
		t.Fatalf("want 2050, got %d", got)
	}
}

func TestLateBatchNotMisjudged(t *testing.T) {
	c := NewCorrector(300)
	c.ObserveBatch(1000, 1500) // offset 500
	c.ObserveBatch(5000, 5500)
	// 迟到批次：发送时刻早但表观偏移与估计一致，不触发修正
	c.ObserveBatch(1200, 1700)
	if c.Adjustments != 0 {
		t.Fatalf("late batch must not trigger adjustment, got %d", c.Adjustments)
	}
	// 迟到条目映射到真实服务端时间
	if got := c.ToServer(1200); got != 1700 {
		t.Fatalf("want 1700, got %d", got)
	}
}
