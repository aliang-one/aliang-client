package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

// TokenHandler handles HTTP requests for token operations
type TokenHandler struct {
	tokenService *services.TokenService
}

// NewTokenHandler creates a new token handler instance with dependency injection
func NewTokenHandler(tokenService *services.TokenService) *TokenHandler {
	return &TokenHandler{
		tokenService: tokenService,
	}
}

// HandleTokenSet handles POST /api/token/set
func (th *TokenHandler) HandleTokenSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}
	th.tokenService.SetToken(req.Token)
	common.Success(w, map[string]string{"token": req.Token})
}

// HandleTokenGet handles GET /api/token/get
func (th *TokenHandler) HandleTokenGet(w http.ResponseWriter, r *http.Request) {
	token := th.tokenService.GetToken()
	common.Success(w, map[string]string{"token": token})
}
