package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// RuleType represents the type of routing rule
type RuleType string

const (
	RuleTypeDomain RuleType = "domain" // Domain matching: *.google.com
	RuleTypeIP     RuleType = "ip"     // IP CIDR matching: 192.168.0.0/16
	RuleTypeGeoIP  RuleType = "geoip"  // GeoIP matching: US, CN (ISO 3166-1 alpha-2)
)

// RoutingRule represents a single routing rule
type RoutingRule struct {
	ID          string    `json:"id" validate:"required,max=128"`
	Type        RuleType  `json:"type" validate:"required,oneof=domain ip geoip"`
	Condition   string    `json:"condition" validate:"required,max=256"`
	Enabled     bool      `json:"enabled" default:"true"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Description string    `json:"description,omitempty" validate:"max=512"`
}

// Validate validates the RoutingRule
func (r *RoutingRule) Validate() error {
	// ID validation
	if r.ID == "" {
		return errors.New("id is required")
	}
	if len(r.ID) > 128 {
		return errors.New("id too long (max 128)")
	}

	// Type validation
	if r.Type != RuleTypeDomain && r.Type != RuleTypeIP && r.Type != RuleTypeGeoIP {
		return errors.New("invalid rule type")
	}

	// Condition validation (depends on Type)
	switch r.Type {
	case RuleTypeDomain:
		if !isValidDomain(r.Condition) {
			return errors.New("invalid domain format")
		}
	case RuleTypeIP:
		if !isValidCIDR(r.Condition) {
			return errors.New("invalid CIDR format")
		}
	case RuleTypeGeoIP:
		if !isValidCountryCode(r.Condition) {
			return errors.New("invalid country code (use ISO 3166-1 alpha-2)")
		}
	}

	// Description validation
	if len(r.Description) > 512 {
		return errors.New("description too long (max 512)")
	}

	return nil
}

// Helper functions for validation
func isValidDomain(domain string) bool {
	// Support wildcard *.example.com and full domain example.com
	if strings.HasPrefix(domain, "*.") {
		domain = domain[2:] // Remove *.
	}
	// Basic DNS domain regex (simplified)
	return regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`).MatchString(domain)
}

func isValidCIDR(cidr string) bool {
	_, _, err := net.ParseCIDR(cidr)
	return err == nil
}

func isValidCountryCode(code string) bool {
	// ISO 3166-1 alpha-2 code (2 uppercase letters)
	return regexp.MustCompile(`^[A-Z]{2}$`).MatchString(code)
}

// SetType represents the type of rule set
type SetType string

const (
	SetTypeToSocks SetType = "to_socks"
	SetTypeDirect  SetType = "direct"
	SetTypeAliang  SetType = "aliang"
)

// RoutingRuleSet represents a collection of routing rules
type RoutingRuleSet struct {
	SetType   SetType       `json:"set_type" validate:"required,oneof=to_socks direct aliang"`
	Rules     []RoutingRule `json:"rules" validate:"dive"`
	Count     int           `json:"count" validate:"min=0,max=10000"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Validate validates the RoutingRuleSet
func (rs *RoutingRuleSet) Validate() error {
	// SetType validation
	if rs.SetType != SetTypeToSocks && rs.SetType != SetTypeDirect && rs.SetType != SetTypeAliang {
		return errors.New("invalid set type")
	}

	// Rules validation
	if len(rs.Rules) > 10000 {
		return errors.New("too many rules (max 10000)")
	}

	// Validate each rule
	for i, rule := range rs.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rule[%d] validation failed: %v", i, err)
		}
	}

	// Count consistency check
	rs.Count = len(rs.Rules)

	return nil
}

// RulesSettings represents global routing settings
type RulesSettings struct {
	AliangEnabled bool      `json:"aliang_enabled" default:"true"`
	SocksEnabled  bool      `json:"socks_enabled" default:"true"`
	GeoIPEnabled  bool      `json:"geoip_enabled" default:"false"`
	AutoUpdate    bool      `json:"auto_update" default:"true"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastNacosSync time.Time `json:"last_nacos_sync,omitempty"`
}

// Validate validates the RulesSettings
func (s *RulesSettings) Validate() error {
	// No additional validation needed - all fields are booleans or timestamps
	return nil
}

// RoutingRulesConfig represents the complete routing configuration
type RoutingRulesConfig struct {
	ToSocks   RoutingRuleSet `json:"to_socks" validate:"required,dive"`
	Direct    RoutingRuleSet `json:"direct" validate:"required,dive"`
	Aliang    RoutingRuleSet `json:"aliang" validate:"required,dive"`
	Settings  RulesSettings  `json:"settings" validate:"required"`
	Version   int            `json:"version" validate:"min=1"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Validate validates the RoutingRulesConfig
func (rc *RoutingRulesConfig) Validate() error {
	// Validate three rule sets
	if err := rc.ToSocks.Validate(); err != nil {
		return fmt.Errorf("to_socks validation failed: %v", err)
	}
	if err := rc.Direct.Validate(); err != nil {
		return fmt.Errorf("direct validation failed: %v", err)
	}
	if err := rc.Aliang.Validate(); err != nil {
		return fmt.Errorf("aliang validation failed: %v", err)
	}

	// Validate settings
	if err := rc.Settings.Validate(); err != nil {
		return fmt.Errorf("settings validation failed: %v", err)
	}

	// Version validation
	if rc.Version < 1 {
		return errors.New("version must be >= 1")
	}

	// Timestamp consistency check
	if rc.UpdatedAt.Before(rc.CreatedAt) {
		return errors.New("updated_at cannot be before created_at")
	}

	return nil
}

// NewRoutingRulesConfig creates a new empty RoutingRulesConfig
func NewRoutingRulesConfig() *RoutingRulesConfig {
	now := time.Now()
	return &RoutingRulesConfig{
		ToSocks: RoutingRuleSet{
			SetType:   SetTypeToSocks,
			Rules:     []RoutingRule{},
			Count:     0,
			UpdatedAt: now,
		},
		Direct: RoutingRuleSet{
			SetType:   SetTypeDirect,
			Rules:     []RoutingRule{},
			Count:     0,
			UpdatedAt: now,
		},
		Aliang: RoutingRuleSet{
			SetType:   SetTypeAliang,
			Rules:     []RoutingRule{},
			Count:     0,
			UpdatedAt: now,
		},
		Settings: RulesSettings{
			AliangEnabled: true,
			SocksEnabled:  true,
			GeoIPEnabled:  false,
			AutoUpdate:    true,
			UpdatedAt:     now,
		},
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ToJSON serializes to JSON
func (rc *RoutingRulesConfig) ToJSON() ([]byte, error) {
	return json.MarshalIndent(rc, "", "  ")
}

// NewRoutingRulesConfigFromJSON deserializes from JSON and validates
func NewRoutingRulesConfigFromJSON(data []byte) (*RoutingRulesConfig, error) {
	var rc RoutingRulesConfig
	if err := json.Unmarshal(data, &rc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	if err := rc.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return &rc, nil
}

// GenerateRuleID generates a rule ID
func GenerateRuleID(ruleType RuleType) string {
	timestamp := time.Now().Unix()
	return fmt.Sprintf("rule_%s_%d", ruleType, timestamp)
}
