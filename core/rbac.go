package core

type Permission string

const (
	PermGraphRun       Permission = "graph:run"
	PermGraphEdit      Permission = "graph:edit"
	PermGraphAdmin     Permission = "graph:admin"
	PermModuleRegister Permission = "module:register"
	PermSecretRead     Permission = "secret:read"
	PermSecretWrite    Permission = "secret:write"
	PermOrganizationAdmin    Permission = "organization:admin"
	// PermPlatformAdmin is the cross-tenant super-admin role. Carriers
	// can see and act on every tenant on the hzd instance — manage
	// keys, list runs, issue keys in any tenant, etc. Distinct from
	// organization:admin (which is per-tenant). For SaaS-style hosting
	// where the operator runs hzd for many customer orgs.
	PermPlatformAdmin Permission = "platform:admin"
)

type Role struct {
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

func (r Role) Has(p Permission) bool {
	for _, have := range r.Permissions {
		if have == p {
			return true
		}
	}
	return false
}
