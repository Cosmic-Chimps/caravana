package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// tokenInfoJSON is an API token as the API shows it (schema TokenInfo). It
// never holds the token's value.
type tokenInfoJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	State      string     `json:"state"`
}

// createdTokenJSON is a new token (schema CreatedToken): the TokenInfo
// fields, inlined, plus the token itself, shown only here.
type createdTokenJSON struct {
	tokenInfoJSON
	Token string `json:"token"`
}

func toTokenInfoJSON(i service.APITokenInfo) tokenInfoJSON {
	return tokenInfoJSON{
		ID: i.ID, Name: i.Name, CreatedAt: i.CreatedAt,
		LastUsedAt: i.LastUsedAt, ExpiresAt: i.ExpiresAt, RevokedAt: i.RevokedAt,
		State: i.State,
	}
}

// createTokenRequest is schema CreateTokenRequest.
type createTokenRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// createToken answers POST /v1/tokens: a new API token, shown only in this
// response.
func (s *server) createToken(w http.ResponseWriter, r *http.Request) error {
	var req createTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	created, err := s.auth.CreateAPIToken(r.Context(), req.Name, req.ExpiresAt)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/tokens/"+created.Info.ID)
	writeJSON(w, http.StatusCreated, createdTokenJSON{tokenInfoJSON: toTokenInfoJSON(created.Info), Token: created.Token})
	return nil
}

// listTokens answers GET /v1/tokens: every token, newest first, never a value.
func (s *server) listTokens(w http.ResponseWriter, r *http.Request) error {
	infos, err := s.auth.ListAPITokens(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		Tokens []tokenInfoJSON `json:"tokens"`
	}{Tokens: make([]tokenInfoJSON, 0, len(infos))}
	for _, i := range infos {
		out.Tokens = append(out.Tokens, toTokenInfoJSON(i))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// revokeToken answers DELETE /v1/tokens/{tokenId}. A request may revoke the
// token it is using.
func (s *server) revokeToken(w http.ResponseWriter, r *http.Request) error {
	if err := s.auth.RevokeAPIToken(r.Context(), r.PathValue("tokenId")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
