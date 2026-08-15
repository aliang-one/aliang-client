package models

// TokenSetRequest is the request body for setting token
type TokenSetRequest struct {
	Token string `json:"token"`
}

// TokenGetResponse is the response body for getting token
type TokenGetResponse struct {
	Token string `json:"token"`
}
