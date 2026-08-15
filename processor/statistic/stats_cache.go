package statistic

import "sync"

// RingBuffer 环形缓冲区，用于存储固定数量的流量统计数据
// 采用FIFO策略，当容量达到上限时，新数据会覆盖最旧的数据
type RingBuffer struct {
	data     []*TrafficStats
	capacity int
	head     int // 下一个写入位置
	tail     int // 最旧数据的位置
	count    int // 当前元素数量
	mu       sync.RWMutex
}

// NewRingBuffer 创建新的环形缓冲区
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		data:     make([]*TrafficStats, capacity),
		capacity: capacity,
		head:     0,
		tail:     0,
		count:    0,
	}
}

// Push 添加新的统计数据到缓冲区
// 如果缓冲区已满，会覆盖最旧的数据
func (rb *RingBuffer) Push(item *TrafficStats) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.data[rb.head] = item
	rb.head = (rb.head + 1) % rb.capacity

	if rb.count < rb.capacity {
		rb.count++
	} else {
		// 缓冲区已满，tail也需要前进
		rb.tail = (rb.tail + 1) % rb.capacity
	}
}

// GetAll 获取所有统计数据，按时间顺序排列（从旧到新）
func (rb *RingBuffer) GetAll() []TrafficStats {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.count == 0 {
		return []TrafficStats{}
	}

	result := make([]TrafficStats, rb.count)
	for i := 0; i < rb.count; i++ {
		idx := (rb.tail + i) % rb.capacity
		if rb.data[idx] != nil {
			result[i] = *rb.data[idx]
		}
	}

	return result
}

// GetLatest 获取最新的统计数据
func (rb *RingBuffer) GetLatest() *TrafficStats {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.count == 0 {
		return nil
	}

	// 最新数据在head-1的位置
	idx := (rb.head - 1 + rb.capacity) % rb.capacity
	return rb.data[idx]
}

// Size 返回当前缓冲区中的元素数量
func (rb *RingBuffer) Size() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

// Clear 清空缓冲区
func (rb *RingBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.data = make([]*TrafficStats, rb.capacity)
	rb.head = 0
	rb.tail = 0
	rb.count = 0
}
