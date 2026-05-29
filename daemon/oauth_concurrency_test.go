package daemon

import (
	"sync"
	"testing"
)

// The registry is mutated at runtime (admin OAuth setup + marketplace
// install/uninstall call Register/Unregister) while the OAuth authorize/callback
// handlers read it. Hammer both sides concurrently so the race detector catches
// any regression that drops the mutex guarding the providers map.
func TestOAuthRegistry_ConcurrentAccess(t *testing.T) {
	r := NewOAuthRegistry("https://app.example.com", nil)
	prov := OAuthProvider{
		Name:         "acme",
		AuthorizeURL: "https://auth.acme.test/authorize",
		TokenURL:     "https://auth.acme.test/token",
		ClientID:     "c",
		ClientSecret: "s",
	}

	var wg sync.WaitGroup
	const n = 50
	for i := 0; i < n; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); r.Register(prov) }()
		go func() { defer wg.Done(); r.Unregister("acme") }()
		go func() {
			defer wg.Done()
			r.Provider("acme")
			_ = r.Providers()
		}()
	}
	wg.Wait()
}
