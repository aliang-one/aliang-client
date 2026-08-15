package statistic

import (
	"container/list"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type HTTPRequestCache struct {
	records  map[string]*list.Element
	order    *list.List
	capacity int
	mu       sync.RWMutex
}

type cacheEntry struct {
	key    string
	record *HTTPRequestRecord
}

func NewHTTPRequestCache(capacity int) *HTTPRequestCache {
	return &HTTPRequestCache{
		records:  make(map[string]*list.Element),
		order:    list.New(),
		capacity: capacity,
	}
}

func (c *HTTPRequestCache) Add(record *HTTPRequestRecord) {
	if record == nil {
		return
	}

	if record.ID == "" {
		record.ID = uuid.New().String()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, exists := c.records[record.ID]; exists {
		c.order.MoveToFront(elem)
		elem.Value.(*cacheEntry).record = record
		return
	}

	entry := &cacheEntry{
		key:    record.ID,
		record: record,
	}

	elem := c.order.PushFront(entry)
	c.records[record.ID] = elem

	if c.order.Len() > c.capacity {
		c.evictOldest()
	}
}

func (c *HTTPRequestCache) evictOldest() {
	if c.order.Len() == 0 {
		return
	}

	oldest := c.order.Back()
	if oldest == nil {
		return
	}

	entry := oldest.Value.(*cacheEntry)
	delete(c.records, entry.key)
	c.order.Remove(oldest)
}

func (c *HTTPRequestCache) Get(id string) *HTTPRequestRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if elem, exists := c.records[id]; exists {
		return elem.Value.(*cacheEntry).record
	}
	return nil
}

func (c *HTTPRequestCache) GetAll() []*HTTPRequestRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*HTTPRequestRecord, 0, c.order.Len())
	for elem := c.order.Front(); elem != nil; elem = elem.Next() {
		result = append(result, elem.Value.(*cacheEntry).record)
	}
	return result
}

func (c *HTTPRequestCache) GetRecent(limit int) []*HTTPRequestRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if limit <= 0 || limit > c.order.Len() {
		limit = c.order.Len()
	}

	result := make([]*HTTPRequestRecord, 0, limit)
	count := 0
	for elem := c.order.Front(); elem != nil && count < limit; elem = elem.Next() {
		result = append(result, elem.Value.(*cacheEntry).record)
		count++
	}
	return result
}

func (c *HTTPRequestCache) GetByHost(host string) []*HTTPRequestRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*HTTPRequestRecord, 0)
	for elem := c.order.Front(); elem != nil; elem = elem.Next() {
		record := elem.Value.(*cacheEntry).record
		if strings.Contains(record.Host, host) {
			result = append(result, record)
		}
	}
	return result
}

func (c *HTTPRequestCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.records = make(map[string]*list.Element)
	c.order = list.New()
}

func (c *HTTPRequestCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.order.Len()
}

func (c *HTTPRequestCache) RemoveOlderThan(maxAge time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	removed := 0

	for {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}

		entry := oldest.Value.(*cacheEntry)
		if entry.record.Timestamp.Before(cutoff) {
			delete(c.records, entry.key)
			c.order.Remove(oldest)
			removed++
		} else {
			break
		}
	}

	return removed
}
