// Package dedup 提供幂等去重与确定性采样。
//
// 去重键由条目内容派生（客户端条目 ID 优先），重试与重复上报命中同一
// 稳定键而被丢弃。采样使用 FNV-1a 哈希：相同输入与相同种子下结果逐次一致。
package dedup

import (
	"hash/fnv"
	"sort"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// Filter 按稳定键去重并按种子确定性采样。
type Filter struct {
	seen map[string]struct{}
	seed uint64
	rate float64 // 采样率 (0,1]，1 表示全量
}

// NewFilter 创建去重采样器。rate 为采样率，seed 为采样种子。
func NewFilter(seed uint64, rate float64) *Filter {
	if rate <= 0 {
		rate = 1e-9
	}
	if rate > 1 {
		rate = 1
	}
	return &Filter{seen: make(map[string]struct{}), seed: seed, rate: rate}
}

// SeenKeys 返回已见键的有序副本（用于持久化，有序保证快照字节稳定）。
func (f *Filter) SeenKeys() []string {
	keys := make([]string, 0, len(f.seen))
	for k := range f.seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LoadSeen 恢复已见键集合。
func (f *Filter) LoadSeen(keys []string) {
	for _, k := range keys {
		f.seen[k] = struct{}{}
	}
}

// Accept 判定条目是否应被计入：重复送达返回 false，采样丢弃返回 false。
func (f *Filter) Accept(e model.Entry) bool {
	key := StableKey(e)
	if _, dup := f.seen[key]; dup {
		return false
	}
	f.seen[key] = struct{}{}
	return f.sample(key)
}

// sample 对键做确定性采样：hash(key, seed) / 2^64 < rate 则保留。
func (f *Filter) sample(key string) bool {
	if f.rate >= 1 {
		return true
	}
	h := fnv.New64a()
	var seedBuf [8]byte
	for i := 0; i < 8; i++ {
		seedBuf[i] = byte(f.seed >> (8 * i))
	}
	h.Write(seedBuf[:])
	h.Write([]byte(key))
	const maxUint64 = float64(^uint64(0))
	return float64(mix64(h.Sum64()))/maxUint64 < f.rate
}

// mix64 是 splitmix64 终态混合，消除 FNV 对短相似键的聚集性。
func mix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// StableKey 计算条目稳定键：优先使用客户端条目 ID，
// 缺失时退化为内容指纹。
func StableKey(e model.Entry) string {
	if e.ID != "" {
		return "id:" + e.ID
	}
	h := fnv.New64a()
	h.Write([]byte(string(e.Kind)))
	h.Write([]byte{0})
	h.Write([]byte(e.Name))
	h.Write([]byte{0})
	h.Write([]byte(e.PageID))
	h.Write([]byte{0})
	h.Write([]byte(e.NavID))
	h.Write([]byte{0})
	var tbuf [8]byte
	for i := 0; i < 8; i++ {
		tbuf[i] = byte(uint64(e.ClientTime) >> (8 * i))
	}
	h.Write(tbuf[:])
	const hexd = "0123456789abcdef"
	sum := h.Sum64()
	var out [16]byte
	for i := 0; i < 16; i++ {
		out[i] = hexd[(sum>>(4*(15-i)))&0xf]
	}
	return "fp:" + string(out[:])
}
