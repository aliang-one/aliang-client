# Draft: Traffic Statistics Enhancement

## Requirements (confirmed)
- **Domain Statistics**: Track access counts and traffic share for preset domains (openai.com, chatgpt.com, claude.com, api.cursor.com)
- **HTTP Request Cache**: Cache 100 HTTP request records with host, path, status code, upload/download token size, first response time, complete response time, model used
- **Traffic Charts**: Provide data for traffic-based and token-based charts over the past 1 hour

## Technical Decisions
- [pending]: Data storage approach (in-memory vs persistent)
- [pending]: Token counting method (streaming vs complete response)
- [pending]: Model extraction approach (from request body vs response)

## Research Findings

### Current Architecture
- **Location**: `processor/statistic/` - existing statistics module
- **Current Stats**: Upload/download bytes, active connections, route-based aggregation
- **Tracker**: `tracker.go` wraps TCP/UDP connections with metadata tracking
- **Manager**: `manager.go` aggregates stats from all trackers
- **Collector**: `collector.go` collects snapshots every 1s with ring buffers (300 slots for 1s/5s/15s)

### Existing Metadata (from `inbound/tun/metadata/metadata.go`)
- HostName, DstIP, DstPort, SrcIP, SrcPort
- Route decision (RouteToCursor, RouteToSocks, RouteDirect)
- DNSInfo with binding source

### Missing Data for Requirements
- HTTP path extraction (currently only host)
- Status code tracking
- Response timing (first byte, complete)
- Token counting (input/output tokens)
- Model extraction from API requests

### API Patterns
- REST endpoints at `/api/stats/traffic/*`
- Polling at 1.5s intervals from frontend
- JSON responses with `common.Success()` wrapper

## Open Questions
1. **Token counting**: Should we parse response bodies for token counts, or use a streaming approach?
2. **Model extraction**: Which API endpoints should we extract models from? (OpenAI, Anthropic, Cursor)
3. **1-hour chart data**: Should we use 15s aggregation (240 data points) or finer granularity?
4. **Domain presets**: Should preset domains be configurable or hardcoded?
5. **Cache eviction**: LRU (oldest first) or time-based (expire after X minutes)?

## Scope Boundaries
- INCLUDE: Domain statistics, request caching, chart data APIs
- INCLUDE: New data structures in processor/statistic/
- INCLUDE: New API endpoints in app/http/handlers/
- EXCLUDE: Frontend UI implementation (data APIs only)
- EXCLUDE: WebSocket (use polling like existing traffic stats)
