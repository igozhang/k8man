package write

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"gitee.com/k8man/internal/api/respond"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

type Handler struct {
	Store *store.Store
	Pool  *kube.Pool
}

func (h *Handler) clients(c *gin.Context) (*kube.Clients, bool) {
	id := c.Param("id")
	cl, err := h.Store.GetCluster(id, true)
	if err != nil {
		respond.Internal(c, err.Error())
		return nil, false
	}
	if cl == nil {
		respond.NotFound(c, "cluster not found")
		return nil, false
	}
	clients, err := h.Pool.Get(cl.ID, cl.Kubeconfig)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return nil, false
	}
	return clients, true
}

func (h *Handler) ctx(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 30*time.Second)
}

func (h *Handler) CreateCluster(c *gin.Context) {
	var in model.ClusterInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Kubeconfig = strings.TrimSpace(in.Kubeconfig)
	if in.Name == "" || in.Kubeconfig == "" {
		respond.BadRequest(c, "name and kubeconfig required")
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	probe := kube.Probe(ctx, in.Kubeconfig)
	now := time.Now().UTC()
	cl := &model.Cluster{
		ID:          uuid.NewString(),
		Name:        in.Name,
		Description: in.Description,
		Kubeconfig:  in.Kubeconfig,
		PermType:    probe.PermType,
		Namespaces:  probe.Namespaces,
		Server:      probe.Server,
		Status:      probe.Status,
		LastError:   probe.LastError,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.Store.CreateCluster(cl); err != nil {
		if store.IsUniqueErr(err) {
			respond.BadRequest(c, "cluster name already exists")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	cl.Kubeconfig = ""
	respond.OK(c, cl)
}

func (h *Handler) UpdateCluster(c *gin.Context) {
	id := c.Param("id")
	existing, err := h.Store.GetCluster(id, true)
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if existing == nil {
		respond.NotFound(c, "cluster not found")
		return
	}
	var in model.ClusterUpdate
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	if in.Name != nil {
		existing.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		existing.Description = *in.Description
	}
	if in.Kubeconfig != nil {
		existing.Kubeconfig = strings.TrimSpace(*in.Kubeconfig)
		ctx, cancel := h.ctx(c)
		defer cancel()
		probe := kube.Probe(ctx, existing.Kubeconfig)
		existing.PermType = probe.PermType
		existing.Namespaces = probe.Namespaces
		existing.Server = probe.Server
		existing.Status = probe.Status
		existing.LastError = probe.LastError
		h.Pool.Invalidate(id)
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := h.Store.UpdateCluster(existing); err != nil {
		if store.IsUniqueErr(err) {
			respond.BadRequest(c, "cluster name already exists")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	existing.Kubeconfig = ""
	respond.OK(c, existing)
}

func (h *Handler) DeleteCluster(c *gin.Context) {
	id := c.Param("id")
	if err := h.Store.DeleteCluster(id); err != nil {
		if err == sql.ErrNoRows {
			respond.NotFound(c, "cluster not found")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	h.Pool.Invalidate(id)
	respond.OK(c, gin.H{"deleted": id})
}

func (h *Handler) RestartPod(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	ns, name := c.Param("ns"), c.Param("name")
	pod, err := clients.GetPod(ctx, ns, name)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	controlled := false
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			controlled = true
			break
		}
	}
	if err := clients.DeletePod(ctx, ns, name); err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	msg := "pod deleted"
	if !controlled {
		msg = "pod deleted; no controller found, it may not be recreated"
	}
	respond.OK(c, gin.H{"message": msg, "name": name})
}

func (h *Handler) ScaleDeployment(c *gin.Context) {
	var in model.ScaleRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	if err := clients.ScaleDeployment(ctx, c.Param("ns"), c.Param("name"), *in.Replicas); err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, gin.H{"name": c.Param("name"), "replicas": *in.Replicas})
}

func (h *Handler) ApplyYAML(c *gin.Context) {
	var in model.YAMLRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	if err := clients.ApplyYAML(ctx, c.Param("ns"), c.Param("kind"), c.Param("name"), in.YAML); err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, gin.H{"updated": c.Param("name")})
}

func (h *Handler) CreateDoc(c *gin.Context) {
	var in model.DocInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	now := time.Now().UTC()
	if in.Category == "" {
		in.Category = "general"
	}
	d := &model.Doc{
		ID: uuid.NewString(), Title: strings.TrimSpace(in.Title), Slug: strings.TrimSpace(in.Slug),
		Category: in.Category, Tags: in.Tags, Summary: in.Summary, Content: in.Content,
		Pinned: in.Pinned, CreatedAt: now, UpdatedAt: now,
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if err := h.Store.CreateDoc(d); err != nil {
		if store.IsUniqueErr(err) {
			respond.BadRequest(c, "slug already exists")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, d)
}

func (h *Handler) UpdateDoc(c *gin.Context) {
	id := c.Param("docId")
	existing, err := h.Store.GetDoc(id)
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if existing == nil {
		respond.NotFound(c, "doc not found")
		return
	}
	var in model.DocInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	existing.Title = strings.TrimSpace(in.Title)
	existing.Slug = strings.TrimSpace(in.Slug)
	if in.Category == "" {
		in.Category = "general"
	}
	existing.Category = in.Category
	existing.Tags = in.Tags
	if existing.Tags == nil {
		existing.Tags = []string{}
	}
	existing.Summary = in.Summary
	existing.Content = in.Content
	existing.Pinned = in.Pinned
	existing.UpdatedAt = time.Now().UTC()
	if err := h.Store.UpdateDoc(existing); err != nil {
		if store.IsUniqueErr(err) {
			respond.BadRequest(c, "slug already exists")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, existing)
}

func (h *Handler) DeleteDoc(c *gin.Context) {
	id := c.Param("docId")
	if err := h.Store.DeleteDoc(id); err != nil {
		if err == sql.ErrNoRows {
			respond.NotFound(c, "doc not found")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, gin.H{"deleted": id})
}
