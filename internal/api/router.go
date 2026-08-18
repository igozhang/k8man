package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"gitee.com/k8man/internal/api/middleware"
	"gitee.com/k8man/internal/api/read"
	"gitee.com/k8man/internal/api/respond"
	"gitee.com/k8man/internal/api/write"
	"gitee.com/k8man/internal/config"
	"gitee.com/k8man/internal/hostsfile"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/store"
)

type UIConfig struct {
	EtcDir string
}

type Deps struct {
	Store   *store.Store
	Pool    *kube.Pool
	Hosts   *hostsfile.File
	WebFS   fs.FS // optional embedded web/dist
	Version string
	UI      UIConfig
}

func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger())

	rh := &read.Handler{Store: d.Store, Pool: d.Pool, Hosts: d.Hosts}
	wh := &write.Handler{Store: d.Store, Pool: d.Pool, Hosts: d.Hosts}

	r.GET("/api/health", func(c *gin.Context) {
		if d.Store != nil {
			if err := d.Store.Ping(); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"status":  "error",
					"version": d.Version,
					"error":   "database",
				})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "version": d.Version})
	})

	r.GET("/api/v1/read/ui", func(c *gin.Context) {
		ui := config.LoadUI(d.UI.EtcDir)
		respond.OK(c, gin.H{
			"title":     ui.Title,
			"tagline":   ui.Tagline,
			"hasBanner": ui.BannerPath != "",
			"bannerUrl": "/banner",
			"version":   d.Version,
		})
	})

	r.GET("/banner", func(c *gin.Context) {
		ui := config.LoadUI(d.UI.EtcDir)
		if ui.BannerPath == "" {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "public, max-age=30")
		c.File(ui.BannerPath)
	})

	readGroup := r.Group("/api/v1/read")
	{
		readGroup.GET("/clusters", rh.ListClusters)
		readGroup.GET("/clusters/:id", rh.GetCluster)
		readGroup.GET("/clusters/:id/namespaces", rh.ListNamespaces)
		readGroup.GET("/clusters/:id/nodes", rh.ListNodes)
		readGroup.GET("/clusters/:id/diagnose", rh.Diagnose)
		readGroup.GET("/clusters/:id/namespaces/:ns/pods", rh.ListPods)
		readGroup.GET("/clusters/:id/namespaces/:ns/pods/:name", rh.GetPod)
		readGroup.GET("/clusters/:id/namespaces/:ns/pods/:name/logs", rh.PodLogs)
		readGroup.GET("/clusters/:id/namespaces/:ns/deployments", rh.ListDeployments)
		readGroup.GET("/clusters/:id/namespaces/:ns/statefulsets", rh.ListStatefulSets)
		readGroup.GET("/clusters/:id/namespaces/:ns/daemonsets", rh.ListDaemonSets)
		readGroup.GET("/clusters/:id/namespaces/:ns/services", rh.ListServices)
		readGroup.GET("/clusters/:id/namespaces/:ns/configmaps", rh.ListConfigMaps)
		readGroup.GET("/clusters/:id/namespaces/:ns/configmaps/:name", rh.GetConfigMap)
		readGroup.GET("/clusters/:id/namespaces/:ns/workloads/:kind/:name/spec-edit", rh.GetWorkloadSpecEdit)
		readGroup.GET("/clusters/:id/namespaces/:ns/resources/:kind/:name/yaml", rh.GetYAML)
		readGroup.GET("/clusters/:id/metrics/nodes", rh.NodeMetrics)
		readGroup.GET("/clusters/:id/metrics/pods", rh.PodMetrics)
		readGroup.GET("/docs", rh.ListDocs)
		readGroup.GET("/docs/:docId", rh.GetDoc)
		readGroup.GET("/hosts", rh.ListHostAliases)
	}

	writeGroup := r.Group("/api/v1/write")
	writeGroup.Use(middleware.AuthWrite())
	{
		writeGroup.POST("/clusters", wh.CreateCluster)
		writeGroup.PUT("/clusters/:id", wh.UpdateCluster)
		writeGroup.DELETE("/clusters/:id", wh.DeleteCluster)
		writeGroup.POST("/clusters/:id/test", wh.TestCluster)
		writeGroup.POST("/clusters/:id/namespaces/:ns/pods/:name/restart", wh.RestartPod)
		writeGroup.PATCH("/clusters/:id/namespaces/:ns/deployments/:name/scale", wh.ScaleDeployment)
		writeGroup.PATCH("/clusters/:id/namespaces/:ns/statefulsets/:name/scale", wh.ScaleStatefulSet)
		writeGroup.PUT("/clusters/:id/namespaces/:ns/resources/:kind/:name/yaml", wh.ApplyYAML)
		writeGroup.PATCH("/clusters/:id/namespaces/:ns/configmaps/:name/data", wh.PatchConfigMapData)
		writeGroup.PATCH("/clusters/:id/namespaces/:ns/workloads/:kind/:name/spec-edit", wh.PatchWorkloadSpecEdit)
		writeGroup.POST("/docs", wh.CreateDoc)
		writeGroup.PUT("/docs/:docId", wh.UpdateDoc)
		writeGroup.DELETE("/docs/:docId", wh.DeleteDoc)
		writeGroup.POST("/hosts", wh.CreateHostAlias)
		writeGroup.PUT("/hosts/:hostId", wh.UpdateHostAlias)
		writeGroup.DELETE("/hosts/:hostId", wh.DeleteHostAlias)
	}

	if d.WebFS != nil {
		staticServer := http.FileServer(http.FS(d.WebFS))
		r.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "not found"})
				return
			}
			f, err := d.WebFS.Open(strings.TrimPrefix(path, "/"))
			if err == nil {
				_ = f.Close()
				staticServer.ServeHTTP(c.Writer, c.Request)
				return
			}
			c.Request.URL.Path = "/"
			staticServer.ServeHTTP(c.Writer, c.Request)
		})
	}

	return r
}
