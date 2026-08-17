package kube

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SpecEditView struct {
	Kind       string           `json:"kind"`
	Name       string           `json:"name"`
	Namespace  string           `json:"namespace"`
	Containers []ContainerEdit  `json:"containers"`
	ConfigMaps []string         `json:"configMaps"`
	Secrets    []string         `json:"secrets"`
}

type ContainerEdit struct {
	Name      string        `json:"name"`
	Image     string        `json:"image"`
	Env       []EnvVarEdit  `json:"env"`
	EnvFrom   []EnvFromEdit `json:"envFrom"`
	Resources ResourceEdit  `json:"resources"`
}

type EnvVarEdit struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvValueFrom `json:"valueFrom,omitempty"`
}

type EnvValueFrom struct {
	Type string `json:"type"` // configMapKeyRef | secretKeyRef | fieldRef | resourceFieldRef | other
	Ref  string `json:"ref"`
}

type EnvFromEdit struct {
	Type     string `json:"type"` // configMapRef | secretRef
	Name     string `json:"name"`
	Optional bool   `json:"optional,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
}

type ResourceEdit struct {
	RequestsCPU    string `json:"requestsCpu"`
	RequestsMemory string `json:"requestsMemory"`
	LimitsCPU      string `json:"limitsCpu"`
	LimitsMemory   string `json:"limitsMemory"`
}

type SpecEditPatch struct {
	Container string         `json:"container"`
	Image     *string        `json:"image"`
	Env       *[]EnvVarEdit  `json:"env"`
	EnvFrom   *[]EnvFromEdit `json:"envFrom"`
	Resources *ResourceEdit  `json:"resources"`
}

func normalizeWorkloadKind(kind string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	switch k {
	case "deployment", "deployments":
		return "deployment", nil
	case "statefulset", "statefulsets":
		return "statefulset", nil
	case "daemonset", "daemonsets":
		return "daemonset", nil
	default:
		return "", fmt.Errorf("kind %q not supported for spec-edit (deployment/statefulset/daemonset)", kind)
	}
}

func (c *Clients) GetWorkloadSpecEdit(ctx context.Context, ns, kind, name string) (*SpecEditView, error) {
	k, err := normalizeWorkloadKind(kind)
	if err != nil {
		return nil, err
	}
	podSpec, err := c.getPodTemplateSpec(ctx, ns, k, name)
	if err != nil {
		return nil, err
	}
	cms, _ := c.ListConfigMapNames(ctx, ns)
	secrets, _ := c.ListSecretNames(ctx, ns)
	if cms == nil {
		cms = []string{}
	}
	if secrets == nil {
		secrets = []string{}
	}
	containers := make([]ContainerEdit, 0, len(podSpec.Containers))
	for _, ctr := range podSpec.Containers {
		containers = append(containers, mapContainerEdit(ctr))
	}
	return &SpecEditView{
		Kind:       k,
		Name:       name,
		Namespace:  ns,
		Containers: containers,
		ConfigMaps: cms,
		Secrets:    secrets,
	}, nil
}

func (c *Clients) PatchWorkloadSpecEdit(ctx context.Context, ns, kind, name string, patch SpecEditPatch) error {
	k, err := normalizeWorkloadKind(kind)
	if err != nil {
		return err
	}
	ctrName := strings.TrimSpace(patch.Container)
	if ctrName == "" {
		return fmt.Errorf("container required")
	}

	switch k {
	case "deployment":
		obj, err := c.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := applyContainerPatch(&obj.Spec.Template.Spec, ctrName, patch); err != nil {
			return err
		}
		_, err = c.Clientset.AppsV1().Deployments(ns).Update(ctx, obj, metav1.UpdateOptions{})
		return err
	case "statefulset":
		obj, err := c.Clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := applyContainerPatch(&obj.Spec.Template.Spec, ctrName, patch); err != nil {
			return err
		}
		_, err = c.Clientset.AppsV1().StatefulSets(ns).Update(ctx, obj, metav1.UpdateOptions{})
		return err
	case "daemonset":
		obj, err := c.Clientset.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := applyContainerPatch(&obj.Spec.Template.Spec, ctrName, patch); err != nil {
			return err
		}
		_, err = c.Clientset.AppsV1().DaemonSets(ns).Update(ctx, obj, metav1.UpdateOptions{})
		return err
	default:
		return fmt.Errorf("unsupported kind")
	}
}

func (c *Clients) getPodTemplateSpec(ctx context.Context, ns, kind, name string) (*corev1.PodSpec, error) {
	switch kind {
	case "deployment":
		obj, err := c.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &obj.Spec.Template.Spec, nil
	case "statefulset":
		obj, err := c.Clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &obj.Spec.Template.Spec, nil
	case "daemonset":
		obj, err := c.Clientset.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &obj.Spec.Template.Spec, nil
	default:
		return nil, fmt.Errorf("unsupported kind")
	}
}

func mapContainerEdit(ctr corev1.Container) ContainerEdit {
	env := make([]EnvVarEdit, 0, len(ctr.Env))
	for _, e := range ctr.Env {
		item := EnvVarEdit{Name: e.Name}
		if e.ValueFrom != nil {
			item.ValueFrom = summarizeValueFrom(e.ValueFrom)
		} else {
			item.Value = e.Value
		}
		env = append(env, item)
	}
	envFrom := make([]EnvFromEdit, 0, len(ctr.EnvFrom))
	for _, ef := range ctr.EnvFrom {
		if ef.ConfigMapRef != nil {
			envFrom = append(envFrom, EnvFromEdit{
				Type:     "configMapRef",
				Name:     ef.ConfigMapRef.Name,
				Optional: ef.ConfigMapRef.Optional != nil && *ef.ConfigMapRef.Optional,
				Prefix:   ef.Prefix,
			})
		} else if ef.SecretRef != nil {
			envFrom = append(envFrom, EnvFromEdit{
				Type:     "secretRef",
				Name:     ef.SecretRef.Name,
				Optional: ef.SecretRef.Optional != nil && *ef.SecretRef.Optional,
				Prefix:   ef.Prefix,
			})
		}
	}
	return ContainerEdit{
		Name:      ctr.Name,
		Image:     ctr.Image,
		Env:       env,
		EnvFrom:   envFrom,
		Resources: mapResourceEdit(ctr.Resources),
	}
}

func summarizeValueFrom(vf *corev1.EnvVarSource) *EnvValueFrom {
	switch {
	case vf.ConfigMapKeyRef != nil:
		return &EnvValueFrom{Type: "configMapKeyRef", Ref: vf.ConfigMapKeyRef.Name + "/" + vf.ConfigMapKeyRef.Key}
	case vf.SecretKeyRef != nil:
		return &EnvValueFrom{Type: "secretKeyRef", Ref: vf.SecretKeyRef.Name + "/" + vf.SecretKeyRef.Key}
	case vf.FieldRef != nil:
		return &EnvValueFrom{Type: "fieldRef", Ref: vf.FieldRef.FieldPath}
	case vf.ResourceFieldRef != nil:
		return &EnvValueFrom{Type: "resourceFieldRef", Ref: vf.ResourceFieldRef.Resource}
	default:
		return &EnvValueFrom{Type: "other", Ref: ""}
	}
}

func mapResourceEdit(rr corev1.ResourceRequirements) ResourceEdit {
	out := ResourceEdit{}
	if rr.Requests != nil {
		if q, ok := rr.Requests[corev1.ResourceCPU]; ok {
			out.RequestsCPU = q.String()
		}
		if q, ok := rr.Requests[corev1.ResourceMemory]; ok {
			out.RequestsMemory = q.String()
		}
	}
	if rr.Limits != nil {
		if q, ok := rr.Limits[corev1.ResourceCPU]; ok {
			out.LimitsCPU = q.String()
		}
		if q, ok := rr.Limits[corev1.ResourceMemory]; ok {
			out.LimitsMemory = q.String()
		}
	}
	return out
}

func applyContainerPatch(spec *corev1.PodSpec, ctrName string, patch SpecEditPatch) error {
	idx := -1
	for i := range spec.Containers {
		if spec.Containers[i].Name == ctrName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("container %q not found", ctrName)
	}
	ctr := &spec.Containers[idx]
	if patch.Image != nil {
		ctr.Image = strings.TrimSpace(*patch.Image)
		if ctr.Image == "" {
			return fmt.Errorf("image cannot be empty")
		}
	}
	if patch.Env != nil {
		built, err := buildEnvVars(*patch.Env, ctr.Env)
		if err != nil {
			return err
		}
		ctr.Env = built
	}
	if patch.EnvFrom != nil {
		built, err := buildEnvFrom(*patch.EnvFrom)
		if err != nil {
			return err
		}
		ctr.EnvFrom = built
	}
	if patch.Resources != nil {
		rr, err := buildResources(*patch.Resources)
		if err != nil {
			return err
		}
		ctr.Resources = rr
	}
	return nil
}

func buildEnvVars(edits []EnvVarEdit, existing []corev1.EnvVar) ([]corev1.EnvVar, error) {
	byName := make(map[string]corev1.EnvVar, len(existing))
	for _, e := range existing {
		byName[e.Name] = e
	}
	out := make([]corev1.EnvVar, 0, len(edits))
	seen := map[string]struct{}{}
	for _, e := range edits {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			// 跳过未填完的空行
			continue
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("duplicate env name %q", name)
		}
		seen[name] = struct{}{}
		if e.ValueFrom != nil {
			orig, ok := byName[name]
			if !ok || orig.ValueFrom == nil {
				return nil, fmt.Errorf("env %q uses valueFrom and cannot be created/changed here; keep original or use YAML", name)
			}
			out = append(out, orig)
			continue
		}
		out = append(out, corev1.EnvVar{Name: name, Value: e.Value})
	}
	return out, nil
}

func buildEnvFrom(edits []EnvFromEdit) ([]corev1.EnvFromSource, error) {
	out := make([]corev1.EnvFromSource, 0, len(edits))
	for _, e := range edits {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			// 跳过未选择引用名的空行
			continue
		}
		src := corev1.EnvFromSource{Prefix: e.Prefix}
		switch strings.ToLower(e.Type) {
		case "configmapref", "configmap":
			src.ConfigMapRef = &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
			}
			if e.Optional {
				opt := true
				src.ConfigMapRef.Optional = &opt
			}
		case "secretref", "secret":
			src.SecretRef = &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
			}
			if e.Optional {
				opt := true
				src.SecretRef.Optional = &opt
			}
		default:
			return nil, fmt.Errorf("envFrom type %q invalid (configMapRef|secretRef)", e.Type)
		}
		out = append(out, src)
	}
	return out, nil
}

func buildResources(edit ResourceEdit) (corev1.ResourceRequirements, error) {
	rr := corev1.ResourceRequirements{}
	req := corev1.ResourceList{}
	lim := corev1.ResourceList{}
	if s := strings.TrimSpace(edit.RequestsCPU); s != "" {
		q, err := resource.ParseQuantity(s)
		if err != nil {
			return rr, fmt.Errorf("requestsCpu: %w", err)
		}
		req[corev1.ResourceCPU] = q
	}
	if s := strings.TrimSpace(edit.RequestsMemory); s != "" {
		q, err := resource.ParseQuantity(s)
		if err != nil {
			return rr, fmt.Errorf("requestsMemory: %w", err)
		}
		req[corev1.ResourceMemory] = q
	}
	if s := strings.TrimSpace(edit.LimitsCPU); s != "" {
		q, err := resource.ParseQuantity(s)
		if err != nil {
			return rr, fmt.Errorf("limitsCpu: %w", err)
		}
		lim[corev1.ResourceCPU] = q
	}
	if s := strings.TrimSpace(edit.LimitsMemory); s != "" {
		q, err := resource.ParseQuantity(s)
		if err != nil {
			return rr, fmt.Errorf("limitsMemory: %w", err)
		}
		lim[corev1.ResourceMemory] = q
	}
	if len(req) > 0 {
		rr.Requests = req
	}
	if len(lim) > 0 {
		rr.Limits = lim
	}
	return rr, nil
}