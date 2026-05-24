package core

type Permission string

const (
	PermGraphRun       Permission = "graph:run"
	PermGraphEdit      Permission = "graph:edit"
	PermGraphAdmin     Permission = "graph:admin"
	PermModuleRegister Permission = "module:register"
	PermSecretRead     Permission = "secret:read"
	PermSecretWrite    Permission = "secret:write"
	PermTenantAdmin    Permission = "tenant:admin"
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
