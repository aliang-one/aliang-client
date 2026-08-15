package rules

import (
	"net/netip"

	"aliang.one/nursorgate/processor/cache"
)

// EvaluationContext contains all information needed for routing decisions
type EvaluationContext struct {
	DstIP    netip.Addr // Destination IP address
	DstPort  uint16     // Destination port
	SrcIP    netip.Addr // Source IP address (optional)
	Domain   string     // Domain name (may be empty for pure IP traffic)
	Protocol string     // Protocol (tcp, udp)
}

// RuleResult represents the result of a routing rule evaluation
type RuleResult struct {
	Route       cache.RouteDecision // Routing decision (cursor/door/direct)
	MatchedRule string              // Name of the rule that matched (for debugging)
	RequiresSNI bool                // Whether SNI extraction is still needed
	Reason      string              // Human-readable explanation of the decision
}
