package kube

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// listViaWatchCache 首页走 API Server watch cache（resourceVersion=0），
// 失败且非权限问题时再回退到 etcd 一致性读。metrics-server 不要走这条。
func listViaWatchCache[T any](limit int64, fn func(metav1.ListOptions) (T, error)) (T, error) {
	v, err := fn(metav1.ListOptions{Limit: limit, ResourceVersion: "0"})
	if err == nil || isNoAccess(err) {
		return v, err
	}
	return fn(metav1.ListOptions{Limit: limit})
}
