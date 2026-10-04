package domain

// OIDCProvider is the OpenID Connect provider set up by the administrator. Users sign in to Laterna
// with their account there (Authelia, Authentik, Keycloak...).
type OIDCProvider struct {
	// Issuer URL ("https://auth.example.org"). Empty means no provider.
	Issuer       string
	ClientID     string
	ClientSecret string
	// Name shown on the sign-in button ("Authelia").
	Name string
	// AutoCreate creates an account for a provider user who has none yet (regular account, all
	// libraries).
	AutoCreate bool
}
