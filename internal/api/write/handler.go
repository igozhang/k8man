package write

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"gitee.com/k8man/internal/alert"
	"gitee.com/k8man/internal/api/respond"
	"gitee.com/k8man/internal/hostsfile"
	"gitee.com/k8man/internal/inspect"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

type Handler struct {
	Store   *store.Store
	Pool    *kube.Pool
	Hosts   *hostsfile.File
	Inspect *inspect.Worker
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

func (h *Handler) syncHosts() {
	if h.Hosts == nil {
		return
	}
	_, _ = h.Hosts.SyncIfChanged()
}

// applyKubeconfigOptions rewrites server / TLS flags. Does not contact the cluster.
func applyKubeconfigOptions(raw, serverOverride string, insecureSkipTLS *bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("kubeconfig required")
	}
	if ov := strings.TrimSpace(serverOverride); ov != "" {
		rewritten, err := kube.OverrideKubeconfigServer(raw, ov)
		if err != nil {
			return "", err
		}
		raw = rewritten
	}
	if insecureSkipTLS != nil {
		rewritten, err := kube.SetKubeconfigInsecureSkipTLS(raw, *insecureSkipTLS)
		if err != nil {
			return "", err
		}
		raw = rewritten
	}
	return raw, nil
}

func probeResponse(probe kube.ProbeResult) gin.H {
	return gin.H{
		"server":      probe.Server,
		"permType":    probe.PermType,
		"namespaces":  probe.Namespaces,
		"status":      probe.Status,
		"lastError":   probe.LastError,
		"frontProxy":  probe.FrontProxy,
		"kubeVersion": probe.KubeVersion,
		"versionHint": probe.VersionHint,
	}
}

func (h *Handler) CreateCluster(c *gin.Context) {
	var in model.ClusterInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	skipTLS := in.InsecureSkipTLS
	prepared, err := applyKubeconfigOptions(in.Kubeconfig, in.ServerOverride, &skipTLS)
	if err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	in.Kubeconfig = prepared
	if in.Name == "" {
		in.Name = kube.SuggestNameFromKubeconfig(in.Kubeconfig)
	}
	if in.Name == "" {
		respond.BadRequest(c, "name required (or provide a kubeconfig with current-context)")
		return
	}
	now := time.Now().UTC()
	cl := &model.Cluster{
		ID:          uuid.NewString(),
		Name:        in.Name,
		Description: in.Description,
		Kubeconfig:  in.Kubeconfig,
		PermType:    "unknown",
		Namespaces:  []string{},
		Server:      kube.SuggestServerFromKubeconfig(in.Kubeconfig),
		Status:      "unknown",
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
	cl.InsecureSkipTLS = kube.KubeconfigInsecureSkipTLS(cl.Kubeconfig)
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
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			respond.BadRequest(c, "name required")
			return
		}
		existing.Name = name
	}
	if in.Description != nil {
		existing.Description = *in.Description
	}
	needRewrite := false
	if in.Kubeconfig != nil {
		existing.Kubeconfig = strings.TrimSpace(*in.Kubeconfig)
		needRewrite = true
	}
	if in.ServerOverride != nil {
		ov := strings.TrimSpace(*in.ServerOverride)
		if ov != "" {
			rewritten, err := kube.OverrideKubeconfigServer(existing.Kubeconfig, ov)
			if err != nil {
				respond.BadRequest(c, err.Error())
				return
			}
			existing.Kubeconfig = rewritten
			needRewrite = true
		}
	}
	if in.InsecureSkipTLS != nil {
		currentSkip := kube.KubeconfigInsecureSkipTLS(existing.Kubeconfig)
		if currentSkip != *in.InsecureSkipTLS {
			rewritten, err := kube.SetKubeconfigInsecureSkipTLS(existing.Kubeconfig, *in.InsecureSkipTLS)
			if err != nil {
				respond.BadRequest(c, err.Error())
				return
			}
			existing.Kubeconfig = rewritten
			needRewrite = true
		}
	}
	if needRewrite {
		existing.Server = kube.SuggestServerFromKubeconfig(existing.Kubeconfig)
		existing.PermType = "unknown"
		existing.Namespaces = []string{}
		existing.Status = "unknown"
		existing.LastError = ""
		existing.FrontProxy = ""
		existing.KubeVersion = ""
		h.Pool.Invalidate(id)
		_ = h.Store.DeleteInspection(id)
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
	existing.InsecureSkipTLS = kube.KubeconfigInsecureSkipTLS(existing.Kubeconfig)
	existing.Kubeconfig = ""
	respond.OK(c, existing)
}

func (h *Handler) ProbeCluster(c *gin.Context) {
	var in model.ClusterProbeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	skipTLS := in.InsecureSkipTLS
	prepared, err := applyKubeconfigOptions(in.Kubeconfig, in.ServerOverride, &skipTLS)
	if err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	h.syncHosts()
	ctx, cancel := h.ctx(c)
	defer cancel()
	probe := kube.Probe(ctx, prepared, h.Pool.Hosts())
	respond.OK(c, probeResponse(probe))
}

func (h *Handler) TestCluster(c *gin.Context) {
	cl, err := h.Store.GetCluster(c.Param("id"), true)
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if cl == nil {
		respond.NotFound(c, "cluster not found")
		return
	}
	h.syncHosts()
	ctx, cancel := h.ctx(c)
	defer cancel()
	probe := kube.Probe(ctx, cl.Kubeconfig, h.Pool.Hosts())
	cl.Server = probe.Server
	cl.PermType = probe.PermType
	cl.Namespaces = probe.Namespaces
	cl.Status = probe.Status
	cl.LastError = probe.LastError
	cl.FrontProxy = probe.FrontProxy
	cl.KubeVersion = probe.KubeVersion
	cl.UpdatedAt = time.Now().UTC()
	if err := h.Store.UpdateCluster(cl); err != nil {
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, probeResponse(probe))
}

func (h *Handler) InspectCluster(c *gin.Context) {
	if h.Inspect == nil {
		respond.Internal(c, "inspect worker not configured")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	out, err := h.Inspect.RunCluster(ctx, c.Param("id"), true)
	if err != nil {
		if errors.Is(err, inspect.ErrNotFound) {
			respond.NotFound(c, "cluster not found")
			return
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			respond.Fail(c, http.StatusGatewayTimeout, 504, "巡检超时")
			return
		}
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, out)
}

func (h *Handler) DeleteCluster(c *gin.Context) {
	id := c.Param("id")
	_ = h.Store.DeleteInspection(id)
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

func (h *Handler) ScaleStatefulSet(c *gin.Context) {
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
	if err := clients.ScaleStatefulSet(ctx, c.Param("ns"), c.Param("name"), *in.Replicas); err != nil {
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

func (h *Handler) PatchConfigMapData(c *gin.Context) {
	var in model.ConfigMapDataRequest
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
	if err := clients.PatchConfigMapData(ctx, c.Param("ns"), c.Param("name"), in.Data); err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, gin.H{"updated": c.Param("name")})
}

func (h *Handler) PatchWorkloadSpecEdit(c *gin.Context) {
	var in kube.SpecEditPatch
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	if strings.TrimSpace(in.Container) == "" {
		respond.BadRequest(c, "container required")
		return
	}
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	if err := clients.PatchWorkloadSpecEdit(ctx, c.Param("ns"), c.Param("kind"), c.Param("name"), in); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "required") ||
			strings.Contains(msg, "not supported") ||
			strings.Contains(msg, "not found") ||
			strings.Contains(msg, "invalid") ||
			strings.Contains(msg, "cannot") ||
			strings.Contains(msg, "duplicate") {
			respond.BadRequest(c, msg)
			return
		}
		respond.Fail(c, http.StatusBadGateway, 502, msg)
		return
	}
	respond.OK(c, gin.H{"updated": c.Param("name"), "container": in.Container})
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

// ExportInspectionReport builds markdown, syncs a copy into 运维文档 (category inspection-report), and returns both.
func (h *Handler) ExportInspectionReport(c *gin.Context) {
	if h.Inspect == nil {
		respond.Internal(c, "inspect worker not configured")
		return
	}
	var body struct {
		Period    string `json:"period"`
		Scope     string `json:"scope"`
		ClusterID string `json:"clusterId"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.Period == "" {
		body.Period = c.Query("period")
	}
	if body.Scope == "" {
		body.Scope = c.Query("scope")
	}
	if body.ClusterID == "" {
		body.ClusterID = c.Query("clusterId")
	}
	rep, err := h.Inspect.BuildExport(inspect.ExportRequest{
		Period:    inspect.ParseExportPeriod(body.Period),
		Scope:     inspect.ParseExportScope(body.Scope),
		ClusterID: strings.TrimSpace(body.ClusterID),
	})
	if err != nil {
		if errors.Is(err, inspect.ErrExportClusterIDRequired) || errors.Is(err, inspect.ErrExportClusterNotFound) {
			respond.BadRequest(c, err.Error())
			return
		}
		respond.Internal(c, err.Error())
		return
	}

	now := time.Now().UTC()
	tags := []string{"巡检报告", rep.Period, rep.Scope}
	summary := strings.TrimSpace(rep.Summary)
	if utf8.RuneCountInString(summary) > 500 {
		r := []rune(summary)
		summary = string(r[:500]) + "…"
	}
	title := strings.TrimSpace(rep.Title)
	if title == "" {
		title = "巡检报告"
	}
	d := &model.Doc{
		ID:        uuid.NewString(),
		Title:     title,
		Category:  inspect.DocCategoryInspectionReport,
		Tags:      tags,
		Summary:   summary,
		Content:   rep.Markdown,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.Store.CreateDoc(d); err != nil {
		respond.Internal(c, "同步运维文档失败: "+err.Error())
		return
	}
	respond.OK(c, gin.H{
		"title":      rep.Title,
		"filename":   rep.Filename,
		"summary":    rep.Summary,
		"markdown":   rep.Markdown,
		"period":     rep.Period,
		"scope":      rep.Scope,
		"windowFrom": rep.WindowFrom,
		"windowTo":   rep.WindowTo,
		"clusterIds": rep.ClusterIDs,
		"doc":        d,
	})
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

func (h *Handler) CreateHostAlias(c *gin.Context) {
	var in model.HostAliasInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	row, err := h.Hosts.Create(in.Hostname, in.IP)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") {
			respond.BadRequest(c, err.Error())
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, row)
}

func (h *Handler) UpdateHostAlias(c *gin.Context) {
	id := c.Param("hostId")
	var in model.HostAliasUpdate
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	hostname := id
	ip := ""
	// Load current values if partial update
	list, err := h.Hosts.List()
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	var cur *model.HostAlias
	for i := range list {
		if list[i].ID == strings.ToLower(id) || list[i].Hostname == strings.ToLower(id) {
			cur = &list[i]
			break
		}
	}
	if cur == nil {
		respond.NotFound(c, "host alias not found")
		return
	}
	hostname = cur.Hostname
	ip = cur.IP
	if in.Hostname != nil {
		hostname = *in.Hostname
	}
	if in.IP != nil {
		ip = *in.IP
	}
	row, err := h.Hosts.Update(id, hostname, ip)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			respond.NotFound(c, "host alias not found")
			return
		}
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") {
			respond.BadRequest(c, err.Error())
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, row)
}

func (h *Handler) DeleteHostAlias(c *gin.Context) {
	id := c.Param("hostId")
	if err := h.Hosts.Delete(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			respond.NotFound(c, "host alias not found")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, gin.H{"deleted": id})
}

func (h *Handler) CreateAlertWebhook(c *gin.Context) {
	var in model.AlertWebhookInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	url := strings.TrimSpace(in.URL)
	if name == "" || url == "" {
		respond.BadRequest(c, "name and url are required")
		return
	}
	if err := alert.ValidateWebhookURL(url); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := time.Now().UTC()
	w := &model.AlertWebhook{
		ID:        uuid.NewString(),
		Name:      name,
		URL:       url,
		Note:      strings.TrimSpace(in.Note),
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.Store.CreateAlertWebhook(w); err != nil {
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, w)
}

func (h *Handler) UpdateAlertWebhook(c *gin.Context) {
	id := c.Param("hookId")
	existing, err := h.Store.GetAlertWebhook(id)
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if existing == nil {
		respond.NotFound(c, "alert webhook not found")
		return
	}
	var in model.AlertWebhookUpdate
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			respond.BadRequest(c, "name is required")
			return
		}
		existing.Name = name
	}
	if in.URL != nil {
		url := strings.TrimSpace(*in.URL)
		if url == "" {
			respond.BadRequest(c, "url is required")
			return
		}
		if err := alert.ValidateWebhookURL(url); err != nil {
			respond.BadRequest(c, err.Error())
			return
		}
		existing.URL = url
	}
	if in.Note != nil {
		existing.Note = strings.TrimSpace(*in.Note)
	}
	if in.Enabled != nil {
		existing.Enabled = *in.Enabled
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := h.Store.UpdateAlertWebhook(existing); err != nil {
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, existing)
}

func (h *Handler) DeleteAlertWebhook(c *gin.Context) {
	id := c.Param("hookId")
	if err := h.Store.DeleteAlertWebhook(id); err != nil {
		if err == sql.ErrNoRows {
			respond.NotFound(c, "alert webhook not found")
			return
		}
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, gin.H{"deleted": id})
}

func (h *Handler) TestAlertWebhook(c *gin.Context) {
	id := c.Param("hookId")
	w, err := h.Store.GetAlertWebhook(id)
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if w == nil {
		respond.NotFound(c, "alert webhook not found")
		return
	}
	if err := alert.ValidateWebhookURL(w.URL); err != nil {
		respond.BadRequest(c, err.Error())
		return
	}
	n := alert.New(h.Store)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if err := n.TestWebhook(ctx, w.URL); err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, gin.H{"ok": true, "id": id})
}
