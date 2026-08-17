package read

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"gitee.com/k8man/internal/api/respond"
	"gitee.com/k8man/internal/hostsfile"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/store"
)

type Handler struct {
	Store *store.Store
	Pool  *kube.Pool
	Hosts *hostsfile.File
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

func (h *Handler) ListClusters(c *gin.Context) {
	list, err := h.Store.ListClusters()
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, list)
}

func (h *Handler) GetCluster(c *gin.Context) {
	cl, err := h.Store.GetCluster(c.Param("id"), false)
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if cl == nil {
		respond.NotFound(c, "cluster not found")
		return
	}
	respond.OK(c, cl)
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
	ctx, cancel := h.ctx(c)
	defer cancel()
	if h.Hosts != nil {
		_, _ = h.Hosts.SyncIfChanged()
	}
	probe := kube.Probe(ctx, cl.Kubeconfig, h.Pool.Hosts())
	cl.Server = probe.Server
	cl.PermType = probe.PermType
	cl.Namespaces = probe.Namespaces
	cl.Status = probe.Status
	cl.LastError = probe.LastError
	cl.UpdatedAt = time.Now().UTC()
	if err := h.Store.UpdateCluster(cl); err != nil {
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, gin.H{
		"server":     probe.Server,
		"permType":   probe.PermType,
		"namespaces": probe.Namespaces,
		"status":     probe.Status,
		"lastError":  probe.LastError,
	})
}

func (h *Handler) ListNamespaces(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.ListNamespaces(ctx)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	type ns struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	out := make([]ns, 0, len(items))
	for _, n := range items {
		out = append(out, ns{Name: n.Name, Status: string(n.Status.Phase)})
	}
	// Prefer stored namespaces for project type if list is empty somehow
	respond.OK(c, out)
}

func (h *Handler) ListNodes(c *gin.Context) {
	cl, err := h.Store.GetCluster(c.Param("id"), true)
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if cl == nil {
		respond.NotFound(c, "cluster not found")
		return
	}
	if cl.PermType == "project" {
		respond.Forbidden(c, "project kubeconfig cannot list nodes")
		return
	}
	clients, err := h.Pool.Get(cl.ID, cl.Kubeconfig)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.ListNodes(ctx)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	type nodeView struct {
		Name             string            `json:"name"`
		Status           string            `json:"status"`
		Roles            string            `json:"roles"`
		Version          string            `json:"version"`
		InternalIP       string            `json:"internalIP"`
		AllocatableCPU   string            `json:"allocatableCPU"`
		AllocatableMem   string            `json:"allocatableMemory"`
		Labels           map[string]string `json:"labels"`
	}
	out := make([]nodeView, 0, len(items))
	for _, n := range items {
		status := "Unknown"
		for _, cond := range n.Status.Conditions {
			if cond.Type == "Ready" {
				if cond.Status == "True" {
					status = "Ready"
				} else {
					status = "NotReady"
				}
			}
		}
		ip := ""
		for _, a := range n.Status.Addresses {
			if a.Type == "InternalIP" {
				ip = a.Address
				break
			}
		}
		out = append(out, nodeView{
			Name:           n.Name,
			Status:         status,
			Version:        n.Status.NodeInfo.KubeletVersion,
			InternalIP:     ip,
			AllocatableCPU: n.Status.Allocatable.Cpu().String(),
			AllocatableMem: n.Status.Allocatable.Memory().String(),
			Labels:         n.Labels,
		})
	}
	respond.OK(c, out)
}

func (h *Handler) ListPods(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	ns := c.Param("ns")
	items, err := clients.ListPods(ctx, ns)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	type podView struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Status    string `json:"status"`
		Ready     string `json:"ready"`
		Restarts  int32  `json:"restarts"`
		Node      string `json:"node"`
		Age       string `json:"age"`
	}
	out := make([]podView, 0, len(items))
	for _, p := range items {
		var ready, total int32
		var restarts int32
		for _, cs := range p.Status.ContainerStatuses {
			total++
			if cs.Ready {
				ready++
			}
			restarts += cs.RestartCount
		}
		out = append(out, podView{
			Name:      p.Name,
			Namespace: p.Namespace,
			Status:    string(p.Status.Phase),
			Ready:     strconv.Itoa(int(ready)) + "/" + strconv.Itoa(int(total)),
			Restarts:  restarts,
			Node:      p.Spec.NodeName,
			Age:       formatAge(p.CreationTimestamp.Time),
		})
	}
	respond.OK(c, out)
}

func (h *Handler) GetPod(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	pod, err := clients.GetPod(ctx, c.Param("ns"), c.Param("name"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, pod)
}

func (h *Handler) PodLogs(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	tail := int64(500)
	if t := c.Query("tail"); t != "" {
		if v, err := strconv.ParseInt(t, 10, 64); err == nil && v > 0 {
			tail = v
		}
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	logs, err := clients.PodLogs(ctx, c.Param("ns"), c.Param("name"), tail)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, gin.H{"logs": logs})
}

func (h *Handler) ListDeployments(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.ListDeployments(ctx, c.Param("ns"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, items)
}

func (h *Handler) ListStatefulSets(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.ListStatefulSets(ctx, c.Param("ns"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, items)
}

func (h *Handler) ListDaemonSets(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.ListDaemonSets(ctx, c.Param("ns"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, items)
}

func (h *Handler) ListServices(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.ListServices(ctx, c.Param("ns"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	type svcView struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Type      string `json:"type"`
		ClusterIP string `json:"clusterIP"`
		Ports     string `json:"ports"`
	}
	out := make([]svcView, 0, len(items))
	for _, s := range items {
		ports := ""
		for i, p := range s.Spec.Ports {
			if i > 0 {
				ports += ","
			}
			ports += p.Name + "/" + strconv.Itoa(int(p.Port))
		}
		out = append(out, svcView{
			Name: s.Name, Namespace: s.Namespace, Type: string(s.Spec.Type),
			ClusterIP: s.Spec.ClusterIP, Ports: ports,
		})
	}
	respond.OK(c, out)
}

func (h *Handler) GetYAML(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	y, err := clients.GetYAML(ctx, c.Param("ns"), c.Param("kind"), c.Param("name"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, gin.H{"yaml": y})
}

func (h *Handler) NodeMetrics(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.NodeMetrics(ctx)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, "metrics unavailable: "+err.Error())
		return
	}
	respond.OK(c, items)
}

func (h *Handler) PodMetrics(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ns := c.Query("namespace")
	if ns == "" {
		ns = c.Param("ns")
	}
	if ns == "" {
		respond.BadRequest(c, "namespace required")
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.PodMetrics(ctx, ns)
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, "metrics unavailable: "+err.Error())
		return
	}
	respond.OK(c, items)
}

func (h *Handler) ListDocs(c *gin.Context) {
	list, err := h.Store.ListDocs(c.Query("category"), c.Query("q"))
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, list)
}

func (h *Handler) GetDoc(c *gin.Context) {
	d, err := h.Store.GetDoc(c.Param("docId"))
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	if d == nil {
		respond.NotFound(c, "doc not found")
		return
	}
	respond.OK(c, d)
}

func (h *Handler) ListHostAliases(c *gin.Context) {
	if h.Hosts != nil {
		_, _ = h.Hosts.SyncIfChanged()
	}
	list, err := h.Hosts.List()
	if err != nil {
		respond.Internal(c, err.Error())
		return
	}
	respond.OK(c, gin.H{
		"path":  h.Hosts.Path(),
		"items": list,
	})
}

func (h *Handler) ListConfigMaps(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	items, err := clients.ListConfigMaps(ctx, c.Param("ns"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, items)
}

func (h *Handler) GetConfigMap(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	item, err := clients.GetConfigMapDetail(ctx, c.Param("ns"), c.Param("name"))
	if err != nil {
		respond.Fail(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	respond.OK(c, item)
}

func (h *Handler) GetWorkloadSpecEdit(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ctx, cancel := h.ctx(c)
	defer cancel()
	item, err := clients.GetWorkloadSpecEdit(ctx, c.Param("ns"), c.Param("kind"), c.Param("name"))
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not supported") {
			respond.BadRequest(c, msg)
			return
		}
		respond.Fail(c, http.StatusBadGateway, 502, msg)
		return
	}
	respond.OK(c, item)
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
	if d < time.Hour {
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	if d < 24*time.Hour {
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}
