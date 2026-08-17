package model

import "time"

type Cluster struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Kubeconfig  string    `json:"kubeconfig,omitempty"`
	PermType    string    `json:"permType"` // cluster | project | unknown
	Namespaces  []string  `json:"namespaces"`
	Server      string    `json:"server"`
	Status      string    `json:"status"` // ok | error
	LastError   string    `json:"lastError,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ClusterInput struct {
	Name           string `json:"name"` // optional; derived from kubeconfig when empty
	Description    string `json:"description"`
	Kubeconfig     string `json:"kubeconfig" binding:"required"`
	ServerOverride string `json:"serverOverride"` // optional; rewrite kubeconfig cluster.server
}

type ClusterUpdate struct {
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	Kubeconfig     *string `json:"kubeconfig"`
	ServerOverride *string `json:"serverOverride"`
}

type Doc struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Slug      string    `json:"slug"`
	Category  string    `json:"category"`
	Tags      []string  `json:"tags"`
	Summary   string    `json:"summary"`
	Content   string    `json:"content"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type DocInput struct {
	Title    string   `json:"title" binding:"required"`
	Slug     string   `json:"slug"`
	Category string   `json:"category"`
	Tags     []string `json:"tags"`
	Summary  string   `json:"summary"`
	Content  string   `json:"content"`
	Pinned   bool     `json:"pinned"`
}

type HostAlias struct {
	ID        string    `json:"id"`
	Hostname  string    `json:"hostname"`
	IP        string    `json:"ip"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type HostAliasInput struct {
	Hostname string `json:"hostname" binding:"required"`
	IP       string `json:"ip" binding:"required"`
	Note     string `json:"note"`
}

type HostAliasUpdate struct {
	Hostname *string `json:"hostname"`
	IP       *string `json:"ip"`
	Note     *string `json:"note"`
}

type ScaleRequest struct {
	Replicas *int32 `json:"replicas" binding:"required"`
}

type YAMLRequest struct {
	YAML string `json:"yaml" binding:"required"`
}

type APIResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}
