package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kyaml "k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	sigsyaml "sigs.k8s.io/yaml"
)

type Clients struct {
	Config    *rest.Config
	Clientset *kubernetes.Clientset
	Dynamic   dynamic.Interface
	Metrics   *metricsv.Clientset
	Server    string
}

type ProbeResult struct {
	Server      string   `json:"server"`
	PermType    string   `json:"permType"` // cluster | project | unknown
	Namespaces  []string `json:"namespaces"`
	Status      string   `json:"status"`
	LastError   string   `json:"lastError"`
	FrontProxy  string   `json:"frontProxy"`  // rancher | non-rancher | empty
	KubeVersion string   `json:"kubeVersion"` // e.g. v1.28.5；空表示未取到
	VersionHint string   `json:"versionHint,omitempty"`
}

type Pool struct {
	mu      sync.RWMutex
	clients map[string]*Clients
	hosts   *HostsResolver
}

func NewPool(hosts *HostsResolver) *Pool {
	if hosts == nil {
		hosts = NewHostsResolver()
	}
	return &Pool{clients: make(map[string]*Clients), hosts: hosts}
}

func (p *Pool) Hosts() *HostsResolver {
	return p.hosts
}

func (p *Pool) Invalidate(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, id)
}

func (p *Pool) InvalidateAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients = make(map[string]*Clients)
}

func (p *Pool) Get(id, kubeconfigYAML string) (*Clients, error) {
	p.mu.RLock()
	if c, ok := p.clients[id]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	c, err := BuildClients(kubeconfigYAML, p.hosts)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Another goroutine may have filled the cache while we built.
	if existing, ok := p.clients[id]; ok {
		return existing, nil
	}
	p.clients[id] = c
	return c, nil
}

// SuggestNameFromKubeconfig returns current-context, else first context/cluster name.
func SuggestNameFromKubeconfig(kubeconfigYAML string) string {
	cfg, err := clientcmd.Load([]byte(kubeconfigYAML))
	if err != nil {
		return ""
	}
	if name := strings.TrimSpace(cfg.CurrentContext); name != "" {
		return name
	}
	for name := range cfg.Contexts {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	for name := range cfg.Clusters {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return ""
}

func BuildClients(kubeconfigYAML string, hosts *HostsResolver) (*Clients, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	prepareRESTConfig(cfg, hosts)
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic: %w", err)
	}
	mc, err := metricsv.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}
	server := cfg.Host
	return &Clients{Config: cfg, Clientset: cs, Dynamic: dyn, Metrics: mc, Server: server}, nil
}

// prepareRESTConfig applies dial/hosts, disables env HTTP(S)_PROXY for API traffic,
// and sets QPS / User-Agent. Private Hosts 映射后若仍走代理，域名请求会被代理拦成 Forbidden。
func prepareRESTConfig(cfg *rest.Config, hosts *HostsResolver) {
	if cfg == nil {
		return
	}
	cfg.Timeout = 30 * time.Second
	if cfg.QPS == 0 {
		cfg.QPS = 30
	}
	if cfg.Burst == 0 {
		cfg.Burst = 60
	}
	cfg.UserAgent = "k8man/1.0"
	// 集群 API / Rancher 入口不应继承进程的 HTTP(S)_PROXY（Hosts 私网解析时尤其如此）。
	cfg.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	wrapGatewayAwareTransport(cfg)
	if hosts != nil {
		hosts.ApplyDial(cfg)
	}
}

func Probe(ctx context.Context, kubeconfigYAML string, hosts *HostsResolver) ProbeResult {
	res := ProbeResult{PermType: "unknown", Status: "error", Namespaces: []string{}}
	c, err := BuildClients(kubeconfigYAML, hosts)
	if err != nil {
		res.LastError = FriendlyConnectivityError(err)
		res.Server = SuggestServerFromKubeconfig(kubeconfigYAML)
		return res
	}
	res.Server = c.Server

	parent := ctx
	frontCh := make(chan string, 1)
	go func() {
		frontCh <- DetectFrontProxy(parent, kubeconfigYAML, hosts)
	}()

	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	nsList, err := listViaWatchCache(500, func(opts metav1.ListOptions) (*corev1.NamespaceList, error) {
		return c.Clientset.CoreV1().Namespaces().List(ctx, opts)
	})
	if err != nil {
		res.KubeVersion, res.VersionHint = readKubeVersion(ctx, c)
		res.LastError = FriendlyConnectivityError(err)
		if kind := waitFront(frontCh); kind != "" {
			res.FrontProxy = kind
		} else {
			res.FrontProxy = classifyFrontFromErr(err)
		}
		return res
	}
	if nsList != nil {
		for _, n := range nsList.Items {
			res.Namespaces = append(res.Namespaces, n.Name)
		}
	}

	res.KubeVersion, res.VersionHint = readKubeVersion(ctx, c)

	_, err = listViaWatchCache(1, func(opts metav1.ListOptions) (*corev1.NodeList, error) {
		return c.Clientset.CoreV1().Nodes().List(ctx, opts)
	})
	if err == nil {
		res.PermType = "cluster"
	} else {
		res.PermType = "project"
	}
	res.Status = "ok"
	res.LastError = ""
	res.FrontProxy = waitFront(frontCh)
	return res
}

// SupportedKubeMinorRange is the practical matrix for Rancher 主流：kubectl/apiserver 约 1.18～1.33。
const (
	supportedKubeMinorMin = 18
	supportedKubeMinorMax = 33
)

func readKubeVersion(ctx context.Context, c *Clients) (version, hint string) {
	if c == nil || c.Clientset == nil {
		return "", ""
	}
	info, err := c.Clientset.Discovery().ServerVersion()
	if err != nil || info == nil {
		return "", ""
	}
	version = strings.TrimSpace(info.GitVersion)
	if version == "" {
		version = strings.TrimSpace(info.Major + "." + info.Minor)
	}
	hint = kubeVersionHint(version)
	return version, hint
}

// kubeVersionHint returns a soft Chinese note when outside the supported minor range.
func kubeVersionHint(gitVersion string) string {
	major, minor, ok := parseKubeMajorMinor(gitVersion)
	if !ok {
		return ""
	}
	if major != 1 {
		return fmt.Sprintf("当前集群版本 %s 不在常见 Rancher/K8s 1.x 范围，读写行为请以实测为准。", gitVersion)
	}
	if minor < supportedKubeMinorMin {
		return fmt.Sprintf("当前集群 %s 低于常用下限 1.%d，部分新字段写回可能失败。", gitVersion, supportedKubeMinorMin)
	}
	if minor > supportedKubeMinorMax {
		return fmt.Sprintf("当前集群 %s 高于已验证上限 1.%d，建议先做 List/日志/伸缩冒烟。", gitVersion, supportedKubeMinorMax)
	}
	return ""
}

func parseKubeMajorMinor(v string) (major, minor int, ok bool) {
	s := strings.TrimSpace(strings.TrimPrefix(v, "v"))
	if s == "" {
		return 0, 0, false
	}
	// 1.28.5+xxx / 1.28+
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return 0, 0, false
	}
	minRaw := parts[1]
	for i, r := range minRaw {
		if r < '0' || r > '9' {
			minRaw = minRaw[:i]
			break
		}
	}
	if minRaw == "" {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(minRaw, "%d", &minor); err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func waitFront(ch <-chan string) string {
	select {
	case v := <-ch:
		return v
	case <-time.After(frontProbeTimeout + 500*time.Millisecond):
		return ""
	}
}

// SuggestServerFromKubeconfig returns the current-context cluster server URL.
func SuggestServerFromKubeconfig(kubeconfigYAML string) string {
	cfg, err := clientcmd.Load([]byte(kubeconfigYAML))
	if err != nil {
		return ""
	}
	ctxName := cfg.CurrentContext
	if ctxName == "" {
		for name := range cfg.Contexts {
			ctxName = name
			break
		}
	}
	if ctxName == "" {
		for _, cluster := range cfg.Clusters {
			if cluster != nil {
				return cluster.Server
			}
		}
		return ""
	}
	ctx := cfg.Contexts[ctxName]
	if ctx == nil {
		return ""
	}
	cluster := cfg.Clusters[ctx.Cluster]
	if cluster == nil {
		return ""
	}
	return cluster.Server
}

// OverrideKubeconfigServer rewrites all cluster server URLs in the kubeconfig.
func OverrideKubeconfigServer(kubeconfigYAML, server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return kubeconfigYAML, nil
	}
	cfg, err := clientcmd.Load([]byte(kubeconfigYAML))
	if err != nil {
		return "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	for _, cluster := range cfg.Clusters {
		if cluster != nil {
			cluster.Server = server
		}
	}
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SetKubeconfigInsecureSkipTLS toggles insecure-skip-tls-verify on all clusters.
// When skip is true, embedded CA data is cleared so client-go does not still verify.
func SetKubeconfigInsecureSkipTLS(kubeconfigYAML string, skip bool) (string, error) {
	cfg, err := clientcmd.Load([]byte(kubeconfigYAML))
	if err != nil {
		return "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	for _, cluster := range cfg.Clusters {
		if cluster == nil {
			continue
		}
		cluster.InsecureSkipTLSVerify = skip
		if skip {
			cluster.CertificateAuthority = ""
			cluster.CertificateAuthorityData = nil
		}
	}
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// KubeconfigInsecureSkipTLS reports whether any cluster has insecure-skip-tls-verify.
func KubeconfigInsecureSkipTLS(kubeconfigYAML string) bool {
	cfg, err := clientcmd.Load([]byte(kubeconfigYAML))
	if err != nil {
		return false
	}
	for _, cluster := range cfg.Clusters {
		if cluster != nil && cluster.InsecureSkipTLSVerify {
			return true
		}
	}
	return false
}

// FriendlyConnectivityError turns common dial/DNS failures into actionable Chinese hints.
func FriendlyConnectivityError(err error) string {
	if err == nil {
		return ""
	}
	var ge *gatewayHTTPError
	if errors.As(err, &ge) {
		kind := ClassifyFrontProxy(ge.StatusCode, ge.Server, ge.ContentType, ge.Body)
		base := ge.Error()
		bl := strings.ToLower(ge.Body)
		switch {
		case kind == FrontNonRancher && (strings.Contains(bl, "default backend")):
			return base + "\n提示：打到了 ingress-nginx 默认后端（default backend），kubeconfig 的路径没有对应 Ingress，或 Host/SNI 不匹配。"
		case kind == FrontNonRancher:
			return base + "\n提示：打到了非 Rancher 前端网关（外置 nginx / F5 / 其它反代等），没有到达 Kubernetes/Rancher API。请确认 Hosts 与 server 域名，以及网关是否把 /k8s/clusters/...（或 /api）反代到后端。"
		default:
			return base + "\n提示：请求打到了前端网关，没有到达 Kubernetes/Rancher API。请确认 Hosts 指向正确入口，且反代已把 /k8s/clusters/...（或 /api）转到后端。"
		}
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"):
		return msg + "\n提示：域名无法解析。请在「KubeConfig 管理」页底部的 Hosts 中添加「IP hostname」，或填写 API Server 覆盖地址。"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return msg + "\n提示：连接超时，请检查 k8man 所在网络能否访问该 API Server（防火墙/路由/安全组）。"
	case strings.Contains(msg, "certificate"), strings.Contains(msg, "x509"), strings.Contains(msg, "unknown authority"):
		return msg + "\n提示：证书校验失败（内网/Rancher 常见）。请编辑该集群并勾选「跳过 TLS 证书校验」后重试。"
	case strings.Contains(msg, "the server could not find the requested resource") && strings.Contains(msg, "namespaces"):
		return msg + "\n提示：对端不像 Kubernetes API（Rancher 反代未生效时，nginx 常返回 HTML 404，client-go 会报这句）。请检查 Hosts 是否指向 Rancher，以及 nginx 是否代理了 /k8s/clusters/..."
	default:
		return msg
	}
}

func (c *Clients) ListNamespaces(ctx context.Context) ([]corev1.Namespace, error) {
	return listViaWatchCache(500, func(opts metav1.ListOptions) ([]corev1.Namespace, error) {
		list, err := c.Clientset.CoreV1().Namespaces().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

func (c *Clients) ListNodes(ctx context.Context) ([]corev1.Node, error) {
	return listViaWatchCache(500, func(opts metav1.ListOptions) ([]corev1.Node, error) {
		list, err := c.Clientset.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

func (c *Clients) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	return listViaWatchCache(1000, func(opts metav1.ListOptions) ([]corev1.Pod, error) {
		list, err := c.Clientset.CoreV1().Pods(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

func (c *Clients) GetPod(ctx context.Context, ns, name string) (*corev1.Pod, error) {
	return c.Clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
}

func (c *Clients) DeletePod(ctx context.Context, ns, name string) error {
	return c.Clientset.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

func (c *Clients) PodLogs(ctx context.Context, ns, name string, tail int64) (string, error) {
	return c.PodLogsQuery(ctx, ns, name, PodLogQuery{Tail: tail})
}

// PodLogQuery is a bounded log read (diagnose / drawer).
type PodLogQuery struct {
	Container  string
	Tail       int64
	Previous   bool
	LimitBytes int64
}

func (c *Clients) PodLogsQuery(ctx context.Context, ns, name string, q PodLogQuery) (string, error) {
	tail := ClampPodLogTail(q.Tail)
	limit := q.LimitBytes
	if limit <= 0 {
		limit = DefaultPodLogBytes
	}
	opts := &corev1.PodLogOptions{
		Container:  q.Container,
		Previous:   q.Previous,
		TailLines:  &tail,
		LimitBytes: &limit,
	}
	req := c.Clientset.CoreV1().Pods(ns).GetLogs(name, opts)
	raw, err := req.Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Clients) ListDeployments(ctx context.Context, ns string) (interface{}, error) {
	return listViaWatchCache(1000, func(opts metav1.ListOptions) (interface{}, error) {
		list, err := c.Clientset.AppsV1().Deployments(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

func (c *Clients) ListStatefulSets(ctx context.Context, ns string) (interface{}, error) {
	return listViaWatchCache(1000, func(opts metav1.ListOptions) (interface{}, error) {
		list, err := c.Clientset.AppsV1().StatefulSets(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

func (c *Clients) ListDaemonSets(ctx context.Context, ns string) (interface{}, error) {
	return listViaWatchCache(1000, func(opts metav1.ListOptions) (interface{}, error) {
		list, err := c.Clientset.AppsV1().DaemonSets(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

// ListReplicaSets returns ReplicaSets in ns (used to resolve Pod → Deployment owners).
func (c *Clients) ListReplicaSets(ctx context.Context, ns string) ([]appsv1.ReplicaSet, error) {
	return listViaWatchCache(1000, func(opts metav1.ListOptions) ([]appsv1.ReplicaSet, error) {
		list, err := c.Clientset.AppsV1().ReplicaSets(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

func (c *Clients) ScaleDeployment(ctx context.Context, ns, name string, replicas int32) error {
	scale, err := c.Clientset.AppsV1().Deployments(ns).GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	scale.Spec.Replicas = replicas
	_, err = c.Clientset.AppsV1().Deployments(ns).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	return err
}

func (c *Clients) ScaleStatefulSet(ctx context.Context, ns, name string, replicas int32) error {
	scale, err := c.Clientset.AppsV1().StatefulSets(ns).GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	scale.Spec.Replicas = replicas
	_, err = c.Clientset.AppsV1().StatefulSets(ns).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	return err
}

func (c *Clients) ListServices(ctx context.Context, ns string) ([]corev1.Service, error) {
	return listViaWatchCache(1000, func(opts metav1.ListOptions) ([]corev1.Service, error) {
		list, err := c.Clientset.CoreV1().Services(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return list.Items, nil
	})
}

var allowedKinds = map[string]schema.GroupVersionResource{
	"pod":          {Group: "", Version: "v1", Resource: "pods"},
	"pods":         {Group: "", Version: "v1", Resource: "pods"},
	"deployment":   {Group: "apps", Version: "v1", Resource: "deployments"},
	"deployments":  {Group: "apps", Version: "v1", Resource: "deployments"},
	"statefulset":  {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"statefulsets": {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"daemonset":    {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"daemonsets":   {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"service":      {Group: "", Version: "v1", Resource: "services"},
	"services":     {Group: "", Version: "v1", Resource: "services"},
	"configmap":    {Group: "", Version: "v1", Resource: "configmaps"},
	"configmaps":   {Group: "", Version: "v1", Resource: "configmaps"},
	"namespace":    {Group: "", Version: "v1", Resource: "namespaces"},
	"namespaces":   {Group: "", Version: "v1", Resource: "namespaces"},
}

func ResolveGVR(kind string) (schema.GroupVersionResource, bool) {
	gvr, ok := allowedKinds[kind]
	return gvr, ok
}

func (c *Clients) GetYAML(ctx context.Context, ns, kind, name string) (string, error) {
	gvr, ok := ResolveGVR(kind)
	if !ok {
		return "", fmt.Errorf("kind %q not allowed", kind)
	}
	var obj *unstructured.Unstructured
	var err error
	if gvr.Resource == "namespaces" {
		obj, err = c.Dynamic.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = c.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return "", err
	}
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
	unstructured.RemoveNestedField(obj.Object, "status")
	b, err := sigsyaml.Marshal(obj.Object)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

var yamlDecoder = kyaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)

func (c *Clients) ApplyYAML(ctx context.Context, ns, kind, name, yamlBody string) error {
	gvr, ok := ResolveGVR(kind)
	if !ok {
		return fmt.Errorf("kind %q not allowed", kind)
	}
	obj := &unstructured.Unstructured{}
	_, _, err := yamlDecoder.Decode([]byte(yamlBody), nil, obj)
	if err != nil {
		return fmt.Errorf("decode yaml: %w", err)
	}
	if obj.GetName() == "" {
		obj.SetName(name)
	}
	if obj.GetName() != name {
		return fmt.Errorf("yaml name %q does not match path name %q", obj.GetName(), name)
	}
	// 写回旧集群时去掉只读/服务端字段，降低 client-go 与 apiserver 版本差带来的 Update 失败。
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
	unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(obj.Object, "metadata", "generation")
	unstructured.RemoveNestedField(obj.Object, "status")

	var existing *unstructured.Unstructured
	if gvr.Resource == "namespaces" {
		existing, err = c.Dynamic.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		_, err = c.Dynamic.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
		return err
	}
	if obj.GetNamespace() == "" {
		obj.SetNamespace(ns)
	}
	existing, err = c.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = c.Dynamic.Resource(gvr).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

func (c *Clients) NodeMetrics(ctx context.Context) (interface{}, error) {
	list, err := c.Metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *Clients) PodMetrics(ctx context.Context, ns string) (interface{}, error) {
	list, err := c.Metrics.MetricsV1beta1().PodMetricses(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// ClassifyMetricsError maps metrics-server / RBAC failures to stable API codes for UI degradation.
// Returns httpStatus, business code, user-facing message.
func ClassifyMetricsError(err error) (int, int, string) {
	if err == nil {
		return http.StatusOK, 0, ""
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return http.StatusForbidden, 403, "insufficient permission to read metrics"
	}
	if apierrors.IsNotFound(err) {
		return http.StatusNotFound, 404, "metrics-server not installed or API unavailable"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "metrics.k8s.io") ||
		strings.Contains(msg, "the server could not find the requested resource") ||
		strings.Contains(msg, "no matches for") ||
		strings.Contains(msg, "could not find the requested resource") {
		return http.StatusNotFound, 404, "metrics-server not installed or API unavailable"
	}
	return http.StatusBadGateway, 502, "metrics unavailable: " + err.Error()
}

// Ensure discovery works (optional health)
func PingAPI(ctx context.Context, c *Clients) error {
	disc, err := discovery.NewDiscoveryClientForConfig(c.Config)
	if err != nil {
		return err
	}
	_, err = disc.ServerVersion()
	return err
}
