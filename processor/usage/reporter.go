package usage

import (
	"fmt"

	"aliang.one/nursorgate/common/logger"
)

// reportBatchMax 单批 records 上限（spec §5.1）。
const reportBatchMax = 500

// Reporter 把本地小时桶推送到服务端。推送经现有 WS 通道 fire-and-forget；
// 服务端按 (device_id, hour_start, model) upsert 整行覆盖，重复推送幂等。
// dirty 清除带 revision 守卫：快照与清除之间 tracker 的并发追加会使清除
// 失效（dirty 保留），该桶下轮重推最新值——定稿桶的尾增量不会丢。
type Reporter struct {
	store    *Store
	deviceID func() string
	tz       string
}

func NewReporter(store *Store, deviceID func() string, tz string) *Reporter {
	return &Reporter{store: store, deviceID: deviceID, tz: tz}
}

// FlushDirty 周期推送：只发 dirty 桶。write 为 WS 发送函数（可为 nil = 未连接）。
func (r *Reporter) FlushDirty(write func(interface{}) error) error {
	return r.flush(write, false)
}

// FlushAll 连接建立后全量补推（含历史未确认桶），成功后清空 dirty。
func (r *Reporter) FlushAll(write func(interface{}) error) error {
	return r.flush(write, true)
}

func (r *Reporter) flush(write func(interface{}) error, all bool) error {
	if write == nil {
		return nil
	}
	var (
		buckets []UsageBucket
		err     error
	)
	if all {
		buckets, err = r.store.AllBuckets()
	} else {
		buckets, err = r.store.DirtyBuckets()
	}
	if err != nil {
		return fmt.Errorf("usage: load buckets: %w", err)
	}
	if len(buckets) == 0 {
		return nil
	}

	var pushedMarks []BucketMark
	for start := 0; start < len(buckets); start += reportBatchMax {
		end := start + reportBatchMax
		if end > len(buckets) {
			end = len(buckets)
		}
		batch := buckets[start:end]
		records := make([]map[string]interface{}, 0, len(batch))
		for _, b := range batch {
			records = append(records, usageRecord(r.deviceID(), b, r.tz))
		}
		payload := map[string]interface{}{
			"type":    "usage.report",
			"records": records,
		}
		if err := write(payload); err != nil {
			// 已成功推出的批次按各自快照 revision 条件清除；失败的批保持
			// dirty 下轮重推（幂等）
			logger.Debug(fmt.Sprintf("usage: report push failed (%d pushed this round): %v", len(pushedMarks), err))
			if len(pushedMarks) > 0 {
				if cerr := r.store.ClearDirtyIfUnchanged(pushedMarks); cerr != nil {
					logger.Debug("usage: clear dirty after partial push: " + cerr.Error())
				}
			}
			return err
		}
		for _, b := range batch {
			pushedMarks = append(pushedMarks, BucketMark{ID: b.ID, Revision: b.Revision})
		}
	}
	return r.store.ClearDirtyIfUnchanged(pushedMarks)
}

func usageRecord(deviceID string, b UsageBucket, tz string) map[string]interface{} {
	return map[string]interface{}{
		"device_id":             deviceID,
		"hour_start":            b.HourStart,
		"tz":                    tz,
		"model":                 b.Model,
		"requests":              b.Requests,
		"input_tokens":          b.InputTokens,
		"output_tokens":         b.OutputTokens,
		"cache_read_tokens":     b.CacheReadTokens,
		"cache_creation_tokens": b.CacheCreationTokens,
		"active_sessions":       b.ActiveSessions,
		"first_seen":            b.FirstSeen,
		"last_seen":             b.LastSeen,
	}
}
