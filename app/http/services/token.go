package services

import (
	"aliang.one/nursorgate/outbound"
)

// TokenService handles token operations
type TokenService struct{}

// NewTokenService creates a new token service instance
func NewTokenService() *TokenService {
	return &TokenService{}
}

// SetToken sets the outbound token
func (ts *TokenService) SetToken(token string) string {
	outbound.SetOutboundToken(token)
	return token
}

// GetToken gets the current outbound token
func (ts *TokenService) GetToken() string {
	return outbound.GetOutboundToken()
}
