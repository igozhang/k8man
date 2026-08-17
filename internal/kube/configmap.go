package kube

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ConfigMapView struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Keys      []string `json:"keys"`
	KeyCount  int      `json:"keyCount"`
	Age       string   `json:"age"`
}

type ConfigMapDetail struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Data       map[string]string `json:"data"`
	BinaryKeys []string          `json:"binaryKeys"`
}

func (c *Clients) ListConfigMaps(ctx context.Context, ns string) ([]ConfigMapView, error) {
	list, err := c.Clientset.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := make([]ConfigMapView, 0, len(list.Items))
	for _, cm := range list.Items {
		keys := make([]string, 0, len(cm.Data)+len(cm.BinaryData))
		for k := range cm.Data {
			keys = append(keys, k)
		}
		for k := range cm.BinaryData {
			keys = append(keys, k)
		}
		out = append(out, ConfigMapView{
			Name:      cm.Name,
			Namespace: cm.Namespace,
			Keys:      keys,
			KeyCount:  len(keys),
			Age:       formatAge(cm.CreationTimestamp.Time),
		})
	}
	return out, nil
}

func (c *Clients) GetConfigMapDetail(ctx context.Context, ns, name string) (*ConfigMapDetail, error) {
	cm, err := c.Clientset.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	data := cm.Data
	if data == nil {
		data = map[string]string{}
	}
	binKeys := make([]string, 0, len(cm.BinaryData))
	for k := range cm.BinaryData {
		binKeys = append(binKeys, k)
	}
	return &ConfigMapDetail{
		Name:       cm.Name,
		Namespace:  cm.Namespace,
		Data:       data,
		BinaryKeys: binKeys,
	}, nil
}

// PatchConfigMapData replaces ConfigMap.data; binaryData is left unchanged.
func (c *Clients) PatchConfigMapData(ctx context.Context, ns, name string, data map[string]string) error {
	if data == nil {
		return fmt.Errorf("data required")
	}
	cm, err := c.Clientset.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	cm.Data = data
	_, err = c.Clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

func (c *Clients) ListSecretNames(ctx context.Context, ns string) ([]string, error) {
	list, err := c.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, s := range list.Items {
		if s.Type == corev1.SecretTypeServiceAccountToken {
			continue
		}
		out = append(out, s.Name)
	}
	return out, nil
}

func (c *Clients) ListConfigMapNames(ctx context.Context, ns string) ([]string, error) {
	list, err := c.Clientset.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, cm := range list.Items {
		out = append(out, cm.Name)
	}
	return out, nil
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
