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
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Kubeconfig  string `json:"kubeconfig" binding:"required"`
}

type ClusterUpdate struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Kubeconfig  *string `json:"kubeconfig"`
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
