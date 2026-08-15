# 日志记录器 - 错误去重功能

## 概述

这个日志记录器实现了智能的错误去重机制，可以有效避免重复错误占用过多带宽。

## 主要功能

### 1. 错误去重
- 使用 MD5 哈希对错误内容进行去重
- 在时间窗口内限制同一错误的发送次数
- 自动清理过期的错误记录

### 2. 可配置参数
- `ErrorWindow`: 错误去重时间窗口（默认5分钟）
- `MaxErrorCount`: 同一错误在时间窗口内的最大发送次数（默认10次）
- `CleanupInterval`: 清理间隔（默认1分钟）

### 3. 内存管理
- 定期清理过期的错误记录
- 使用读写锁保证线程安全
- 优雅关闭时清理资源

## 使用方法

### 基本使用
```go
import "your-project/common/logger"

// 初始化
err := logger.Init()
if err != nil {
    panic(err)
}
defer logger.Shutdown()

// 记录日志
logger.Info("应用启动")
logger.Error("发生错误")
logger.Warn("警告信息")
```

### 自定义配置
```go
config := &logger.ErrorDedupConfig{
    ErrorWindow:     10 * time.Minute, // 10分钟窗口
    MaxErrorCount:   5,                // 最多发送5次
    CleanupInterval: 2 * time.Minute,  // 每2分钟清理一次
}
logger.SetErrorDedupConfig(config)
```

## 工作原理

1. **错误哈希**: 对错误内容生成 MD5 哈希值
2. **时间窗口**: 在指定时间窗口内跟踪错误出现次数
3. **限流机制**: 超过最大次数后不再发送到 Sentry
4. **自动清理**: 定期清理过期的错误记录

## 优势

- **节省带宽**: 避免重复错误占用网络资源
- **减少噪音**: 减少 Sentry 中的重复错误报告
- **可配置**: 根据实际需求调整参数
- **线程安全**: 使用读写锁保证并发安全
- **内存友好**: 自动清理过期数据

## 注意事项

- 错误去重基于错误内容的哈希值，相同内容的错误会被去重
- 时间窗口结束后，错误计数会重置
- 建议根据实际错误频率调整配置参数 