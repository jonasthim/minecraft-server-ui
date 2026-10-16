package auth

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/url"
	"strings"
)

func (s *Service) GetSettings(ctx context.Context) (domain.Settings, error) {
	v, e := s.settings.Get(ctx)
	v.Auth.OIDC.ClientSecret = ""
	v.Auth.OIDC.RedirectURI = strings.TrimRight(s.cfg.BaseURL, "/") + oidcCallbackPath
	return v, e
}
func (s *Service) PutSettings(ctx context.Context, in domain.Settings) (domain.Settings, error) {
	current, e := s.settings.Get(ctx)
	if e != nil {
		return domain.Settings{}, e
	}
	o := &in.Auth.OIDC
	if o.ClientSecret == "" {
		o.ClientSecret = current.Auth.OIDC.ClientSecret
	}
	if o.ProviderName == "" {
		o.ProviderName = "SSO"
	}
	if o.GroupsClaim == "" {
		o.GroupsClaim = "groups"
	}
	if o.DefaultRole == "" {
		o.DefaultRole = "deny"
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{"openid", "profile", "email", "groups"}
	}
	if o.DefaultRole != "deny" && !domain.Role(o.DefaultRole).Valid() {
		return domain.Settings{}, domain.E(domain.CodeValidationFailed, "invalid default role")
	}
	for _, role := range o.RoleMapping {
		if !role.Valid() {
			return domain.Settings{}, domain.E(domain.CodeValidationFailed, "invalid group role mapping")
		}
	}
	if !in.Auth.LocalLoginEnabled && !o.Enabled {
		return domain.Settings{}, domain.E(domain.CodeValidationFailed, "at least one login method must remain enabled")
	}
	if o.Enabled {
		u, e := url.Parse(o.IssuerURL)
		if e != nil || u.Host == "" || (u.Scheme != "https" && !(s.cfg.InsecureCookies && u.Scheme == "http")) {
			return domain.Settings{}, domain.E(domain.CodeValidationFailed, "OIDC issuer must be an HTTPS URL")
		}
		if o.ClientID == "" || o.ClientSecret == "" || s.cfg.BaseURL == "" {
			return domain.Settings{}, domain.E(domain.CodeValidationFailed, "OIDC requires client ID, client secret, and configured base URL")
		}
	}
	o.RedirectURI = ""
	if e = s.settings.Put(ctx, in); e != nil {
		return domain.Settings{}, e
	}
	s.InvalidateOIDC()
	return s.GetSettings(ctx)
}
