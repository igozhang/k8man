package kube

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	Server     string
	PermType   string // cluster | project | unknown
	Namespaces []string
	Status     string
	LastError  string
}

type Pool struct {
	mu      sync.RWMutex
	clients map[string]*Clients
}

func NewPool() *Pool {
	return &Pool{clients: make(map[string]*Clients)}
}

func (p *Pool) Invalidate(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, id)
}

func (p *Pool) Get(id, kubeconfigYAML string) (*Clients, error) {
	p.mu.RLock()
	if c, ok := p.clients[id]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	c, err := BuildClients(kubeconfigYAML)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.clients[id] = c
	p.mu.Unlock()
	return c, nil
}

func BuildClients(kubeconfigYAML string) (*Clients, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	cfg.Timeout = 30 * time.Second
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

func Probe(ctx context.Context, kubeconfigYAML string) ProbeResult {
	res := ProbeResult{PermType: "unknown", Status: "error", Namespaces: []string{}}
	c, err := BuildClients(kubeconfigYAML)
	if err != nil {
		res.LastError = err.Error()
		return res
	}
	res.Server = c.Server

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	nsList, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		res.LastError = err.Error()
		return res
	}
	for _, n := range nsList.Items {
		res.Namespaces = append(res.Namespaces, n.Name)
	}

	_, err = c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil {
		res.PermType = "cluster"
	} else {
		res.PermType = "project"
	}
	res.Status = "ok"
	res.LastError = ""
	return res
}

func (c *Clients) ListNamespaces(ctx context.Context) ([]corev1.Namespace, error) {
	list, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *Clients) ListNodes(ctx context.Context) ([]corev1.Node, error) {
	list, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *Clients) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	list, err := c.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *Clients) GetPod(ctx context.Context, ns, name string) (*corev1.Pod, error) {
	return c.Clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
}

func (c *Clients) DeletePod(ctx context.Context, ns, name string) error {
	return c.Clientset.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

func (c *Clients) PodLogs(ctx context.Context, ns, name string, tail int64) (string, error) {
	opts := &corev1.PodLogOptions{TailLines: &tail}
	req := c.Clientset.CoreV1().Pods(ns).GetLogs(name, opts)
	raw, err := req.Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *Clients) ListDeployments(ctx context.Context, ns string) (interface{}, error) {
	list, err := c.Clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
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

func (c *Clients) ListServices(ctx context.Context, ns string) ([]corev1.Service, error) {
	list, err := c.Clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

var allowedKinds = map[string]schema.GroupVersionResource{
	"pod":        {Group: "", Version: "v1", Resource: "pods"},
	"pods":       {Group: "", Version: "v1", Resource: "pods"},
	"deployment": {Group: "apps", Version: "v1", Resource: "deployments"},
	"deployments":{Group: "apps", Version: "v1", Resource: "deployments"},
	"service":    {Group: "", Version: "v1", Resource: "services"},
	"services":   {Group: "", Version: "v1", Resource: "services"},
	"namespace":  {Group: "", Version: "v1", Resource: "namespaces"},
	"namespaces": {Group: "", Version: "v1", Resource: "namespaces"},
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

// Ensure discovery works (optional health)
func PingAPI(ctx context.Context, c *Clients) error {
	disc, err := discovery.NewDiscoveryClientForConfig(c.Config)
	if err != nil {
		return err
	}
	_, err = disc.ServerVersion()
	return err
}
