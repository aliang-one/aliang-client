package logger

import (
	"sync"
	"time"
)

const DefaultLogBufferSize = 1000

// LogEntry represents a single log entry
type LogEntry struct {
	Level     LogLevelType
	Timestamp time.Time
	Message   string
	Source    string // "main", "http", "singbox", "export"
	TraceID   string // Connection trace ID if applicable
}

// observerEntry pairs an observer function with a unique ID for unsubscribing.
type observerEntry struct {
	fn func(*LogEntry)
	id uint64
}

// LogBuffer is a thread-safe circular buffer for log entries
type LogBuffer struct {
	entries   []*LogEntry
	size      int
	head      int
	tail      int
	isFull    bool
	mu        sync.RWMutex
	maxSize   int
	observers []observerEntry
	nextObsID uint64
}

// NewLogBuffer creates a new log buffer with specified max size
func NewLogBuffer(maxSize int) *LogBuffer {
	if maxSize <= 0 {
		maxSize = DefaultLogBufferSize
	}
	return &LogBuffer{
		entries: make([]*LogEntry, maxSize),
		maxSize: maxSize,
		head:    0,
		tail:    0,
		isFull:  false,
	}
}

// Append adds a new log entry to the buffer
func (b *LogBuffer) Append(entry *LogEntry) {
	if entry == nil {
		return
	}

	b.mu.Lock()

	b.entries[b.tail] = entry
	b.tail = (b.tail + 1) % b.maxSize

	if b.isFull {
		b.head = (b.head + 1) % b.maxSize
	} else if b.tail == b.head {
		b.isFull = true
	}

	observers := append([]observerEntry(nil), b.observers...)
	b.mu.Unlock()

	// Notify observers outside the buffer lock.
	for _, obs := range observers {
		obs.fn(entry)
	}
}

// Get returns log entries with filtering options
// limit: max number of entries to return (0 = all)
// level: filter by level (0 = all levels)
// source: filter by source ("" = all sources)
func (b *LogBuffer) Get(limit int, level LogLevelType, source string) []*LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var result []*LogEntry

	if b.isFull {
		// Iterate from head to tail (full buffer)
		for i := 0; i < b.maxSize; i++ {
			idx := (b.head + i) % b.maxSize
			entry := b.entries[idx]
			if entry != nil && b.matchesFilter(entry, level, source) {
				result = append(result, entry)
			}
		}
	} else {
		// Iterate from 0 to tail (partial buffer)
		for i := 0; i < b.tail; i++ {
			entry := b.entries[i]
			if entry != nil && b.matchesFilter(entry, level, source) {
				result = append(result, entry)
			}
		}
	}

	// Apply limit (from the end)
	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}

	// Return copy to prevent external modification
	copiedResult := make([]*LogEntry, len(result))
	copy(copiedResult, result)
	return copiedResult
}

// GetAll returns all log entries
func (b *LogBuffer) GetAll() []*LogEntry {
	return b.Get(0, 0, "")
}

// GetRecent returns the last N entries
func (b *LogBuffer) GetRecent(count int) []*LogEntry {
	return b.Get(count, 0, "")
}

// Filter returns entries matching the filter criteria
func (b *LogBuffer) Filter(level LogLevelType, source string) []*LogEntry {
	return b.Get(0, level, source)
}

// Clear removes all entries from buffer
func (b *LogBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.entries = make([]*LogEntry, b.maxSize)
	b.head = 0
	b.tail = 0
	b.isFull = false
}

// Size returns current number of entries in buffer
func (b *LogBuffer) Size() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.isFull {
		return b.maxSize
	}
	return b.tail
}

// Subscribe registers an observer for new log entries and returns an unsubscribe function.
func (b *LogBuffer) Subscribe(observer func(*LogEntry)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextObsID
	b.nextObsID++
	b.observers = append(b.observers, observerEntry{fn: observer, id: id})
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i := range b.observers {
			if b.observers[i].id == id {
				b.observers = append(b.observers[:i], b.observers[i+1:]...)
				return
			}
		}
	}
}

// matchesFilter checks if entry matches filter criteria
func (b *LogBuffer) matchesFilter(entry *LogEntry, level LogLevelType, source string) bool {
	// If level is 0 (default), include all levels
	if level != 0 && entry.Level != level {
		return false
	}
	// If source is empty, include all sources
	if source != "" && entry.Source != source {
		return false
	}
	return true
}

// Global log buffer instance
var globalBuffer = NewLogBuffer(DefaultLogBufferSize)

// GetGlobalBuffer returns the global log buffer
func GetGlobalBuffer() *LogBuffer {
	return globalBuffer
}

// AppendToBuffer appends an entry to the global buffer
func AppendToBuffer(entry *LogEntry) {
	globalBuffer.Append(entry)
}

// GetBufferEntries retrieves entries from the global buffer
func GetBufferEntries(limit int, level LogLevelType, source string) []*LogEntry {
	return globalBuffer.Get(limit, level, source)
}

// ClearBuffer clears the global buffer
func ClearBuffer() {
	globalBuffer.Clear()
}
