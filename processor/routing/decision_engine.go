package routing

import (
	"fmt"
	"net/netip"
	"strings"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/model"
)

// T028: Route decision types (3 constants)
type RouteDecision string

const (
	RouteToAliang RouteDecision = "aliang"
	RouteToSocks  RouteDecision = "socks"
	RouteDirect   RouteDecision = "direct"
	RouteDeny     RouteDecision = "deny"
)

// T029: Matching context for route decision
type MatchContext struct {
	Domain string // Domain name (e.g., "example.com")
	IP     string // IP address (e.g., "1.2.3.4")
	// Request can hold additional request information if needed
	Request map[string]interface{}
}

// T032-T033: DecideRoute determines which proxy to use based on rules and priority
// Priority order:
// 1. Aliang rules (highest) - if enabled
// 2. SOCKS rules (to_socks) - if enabled
// 3. GeoIP rules - if enabled
// 4. Direct (lowest) - default fallback
func DecideRoute(config *model.RoutingRulesConfig, ctx *MatchContext) (RouteDecision, error) {
	if config == nil {
		logger.Warn("RoutingRulesConfig is nil, using default Direct routing")
		return RouteDirect, nil
	}

	if ctx == nil {
		logger.Warn("MatchContext is nil, using default Direct routing")
		return RouteDirect, nil
	}

	snapshot, err := CompileRuntimeSnapshotFromLegacyConfig(config)
	if err != nil {
		return RouteDirect, fmt.Errorf("compile runtime snapshot failed: %w", err)
	}

	return DecideRouteFromSnapshot(snapshot, ctx)
}

func DecideRouteFromSnapshot(snapshot *RuntimeSnapshot, ctx *MatchContext) (RouteDecision, error) {
	if snapshot == nil {
		logger.Warn("RuntimeSnapshot is nil, using default Direct routing")
		return RouteDirect, nil
	}

	if ctx == nil {
		logger.Warn("MatchContext is nil, using default Direct routing")
		return RouteDirect, nil
	}

	action := snapshot.DefaultAction()
	capabilities := snapshot.BranchCapabilities()
	for _, rule := range snapshot.Rules() {
		if !rule.Enabled() {
			continue
		}
		if checkRuleBySnapshot(rule, ctx) {
			if isSnapshotActionAvailable(rule.Target(), capabilities) {
				action = rule.Target()
				break
			}
			if snapshot.StrictUnavailableBranchDeny() {
				action = snapshot.ExplicitDenyAction()
				break
			}
		}
	}

	return resolveSnapshotAction(snapshot, action), nil

}

func resolveSnapshotAction(snapshot *RuntimeSnapshot, action SnapshotAction) RouteDecision {
	capabilities := snapshot.BranchCapabilities()
	resolved := action

	switch action {
	case SnapshotActionToAliang:
		if !capabilities.ToAliang() {
			if snapshot.StrictUnavailableBranchDeny() {
				resolved = snapshot.ExplicitDenyAction()
			} else {
				resolved = SnapshotActionDirect
			}
		}
	case SnapshotActionToSocks:
		if !capabilities.ToSocks() {
			if snapshot.StrictUnavailableBranchDeny() {
				resolved = snapshot.ExplicitDenyAction()
			} else {
				resolved = SnapshotActionDirect
			}
		}
	case SnapshotActionDirect:
		if !capabilities.Direct() {
			if snapshot.StrictUnavailableBranchDeny() {
				resolved = snapshot.ExplicitDenyAction()
			} else {
				resolved = SnapshotActionDirect
			}
		}
	}

	switch resolved {
	case SnapshotActionToAliang:
		return RouteToAliang
	case SnapshotActionToSocks:
		return RouteToSocks
	case SnapshotActionDeny:
		return RouteDeny
	default:
		return RouteDirect
	}
}

func isSnapshotActionAvailable(action SnapshotAction, capabilities SnapshotBranchCapabilities) bool {
	switch action {
	case SnapshotActionToAliang:
		return capabilities.ToAliang()
	case SnapshotActionToSocks:
		return capabilities.ToSocks()
	case SnapshotActionDirect:
		return capabilities.Direct()
	case SnapshotActionDeny:
		return true
	default:
		return false
	}
}

// checkRuleSet checks if any rule in the rule set matches the context
// Returns the routing decision if a match is found, nil otherwise
func checkRuleSet(ruleSet *model.RoutingRuleSet, ctx *MatchContext, routeDecision RouteDecision) *RouteDecision {
	if ruleSet == nil || len(ruleSet.Rules) == 0 {
		return nil
	}

	for _, rule := range ruleSet.Rules {
		if !rule.Enabled {
			// Skip disabled rules (T027)
			continue
		}

		if checkRule(&rule, ctx) {
			logger.Debug(fmt.Sprintf("Rule %s matched (type: %s, condition: %s)", rule.ID, rule.Type, rule.Condition))
			return &routeDecision
		}
	}

	return nil
}

func checkRuleBySnapshot(rule SnapshotRule, ctx *MatchContext) bool {
	switch rule.Type() {
	case string(model.RuleTypeDomain):
		return matchDomain(rule.Condition(), ctx.Domain)
	case string(model.RuleTypeIP):
		return matchIP(rule.Condition(), ctx.IP)
	case string(model.RuleTypeGeoIP):
		return matchGeoIP(rule.Condition(), ctx.IP)
	default:
		logger.Warn(fmt.Sprintf("Unknown rule type: %s", rule.Type()))
		return false
	}
}

// checkRule determines if a single rule matches the context
func checkRule(rule *model.RoutingRule, ctx *MatchContext) bool {
	if rule == nil || rule.Condition == "" {
		return false
	}

	switch rule.Type {
	case model.RuleTypeDomain:
		// Domain matching with wildcard support (T030)
		return matchDomain(rule.Condition, ctx.Domain)

	case model.RuleTypeIP:
		// IP range matching with CIDR support (T031)
		return matchIP(rule.Condition, ctx.IP)

	case model.RuleTypeGeoIP:
		// GeoIP country code matching
		return matchGeoIP(rule.Condition, ctx.IP)

	default:
		logger.Warn(fmt.Sprintf("Unknown rule type: %s", rule.Type))
		return false
	}
}

// T030: matchDomain checks if a domain matches a pattern (supports wildcards)
// Examples:
// - "example.com" matches "example.com"
// - "example.com" matches "www.example.com", "mail.example.com", etc.
// - "*.example.com" matches "www.example.com", "mail.example.com", etc.
// - "*.example.com" does NOT match "example.com"
func matchDomain(pattern, domain string) bool {
	if pattern == "" || domain == "" {
		return false
	}

	// Exact match
	if pattern == domain {
		return true
	}

	// Plain domain rules are treated as suffix rules for customer-facing
	// entries like "domains,google.com".
	if strings.HasSuffix(domain, "."+pattern) {
		return true
	}

	// Wildcard matching: *.example.com
	if len(pattern) > 2 && pattern[0] == '*' && pattern[1] == '.' {
		suffix := pattern[1:] // ".example.com"
		// Check if domain ends with the suffix (e.g., domain = "www.example.com")
		if len(domain) > len(suffix) && domain[len(domain)-len(suffix):] == suffix {
			return true
		}
	}

	return false
}

// T031: matchIP checks if an IP address matches a CIDR range
// Examples:
// - "192.168.0.0/16" matches "192.168.1.1", "192.168.255.255", etc.
// - "10.0.0.0/8" matches any IP in 10.0.0.0 to 10.255.255.255
func matchIP(cidr, ip string) bool {
	if cidr == "" || ip == "" {
		return false
	}

	// Parse CIDR range
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		logger.Warn(fmt.Sprintf("Invalid CIDR range %s: %v", cidr, err))
		return false
	}

	// Parse IP address
	ipAddr, err := netip.ParseAddr(ip)
	if err != nil {
		logger.Warn(fmt.Sprintf("Invalid IP address %s: %v", ip, err))
		return false
	}

	// Check if IP is in the CIDR range
	return prefix.Contains(ipAddr)
}

// matchGeoIP checks if an IP's country code matches the rule condition
// This is a placeholder for now - actual implementation will use GeoIP service
// Examples:
// - Rule condition "US" matches IPs from United States
// - Rule condition "CN" matches IPs from China
func matchGeoIP(countryCode, ip string) bool {
	if countryCode == "" || ip == "" {
		return false
	}

	// TODO(US2-Phase4): Integrate with GeoIP service
	// For now, this is a stub that will be fully implemented when GeoIP service is integrated
	logger.Debug(fmt.Sprintf("GeoIP matching not yet implemented for %s (country: %s)", ip, countryCode))
	return false
}

// String representation for RouteDecision
func (r RouteDecision) String() string {
	return string(r)
}
