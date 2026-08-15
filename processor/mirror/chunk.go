package mirror

// Direction indicates the data flow direction of a StreamChunk.
type Direction string

const (
	DirectionRequest  Direction = "request"  // client -> upstream
	DirectionResponse Direction = "response" // upstream -> client
)

// StreamChunk represents a single TCP data fragment captured during mirroring.
// The receiver can reconstruct a stable byte stream using flow_id + direction + offset.
// EventType is empty for data chunks; non-empty values ("flow_start"/"flow_end") are
// only used in FlowEvent messages. Kept here for forward compatibility.
type StreamChunk struct {
	EventType    string    `json:"event_type,omitempty"` // empty for data chunks
	FlowID       string    `json:"flow_id"`
	ConnID       string    `json:"conn_id,omitempty"`
	Direction    Direction `json:"direction"`
	Offset       uint64    `json:"offset"`
	Seq          uint64    `json:"seq"`
	Payload      []byte    `json:"payload"`
	Timestamp    int64     `json:"timestamp"`
	SrcAddr      string    `json:"src_addr"`
	DstAddr      string    `json:"dst_addr"`
	ClientAddr   string    `json:"client_addr"`
	UpstreamAddr string    `json:"upstream_addr"`
	ProtocolHint string    `json:"protocol_hint,omitempty"`
}
