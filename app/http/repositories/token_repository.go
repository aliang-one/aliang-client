package repositories

import (
	"aliang.one/nursorgate/app/http/services"
)

// TokenRepositoryImpl provides access to token functionality
type TokenRepositoryImpl struct {
	tokenService *services.TokenService
}

// NewTokenRepository creates a new token repository instance
func NewTokenRepository() *TokenRepositoryImpl {
	return &TokenRepositoryImpl{
		tokenService: services.NewTokenService(),
	}
}

// SetToken sets the outbound token
func (tr *TokenRepositoryImpl) SetToken(token string) string {
	return tr.tokenService.SetToken(token)
}

// GetToken gets the current outbound token
func (tr *TokenRepositoryImpl) GetToken() string {
	return tr.tokenService.GetToken()
}
