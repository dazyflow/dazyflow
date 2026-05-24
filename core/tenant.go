package core

type Tenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Workspace struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	GitURL   string `json:"git_url"`
}
