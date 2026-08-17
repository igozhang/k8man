package kube

import (
	"fmt"
	"net/http"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestClassifyMetricsError(t *testing.T) {
	st, code, msg := ClassifyMetricsError(nil)
	if st != http.StatusOK || code != 0 || msg != "" {
		t.Fatalf("nil: got %d %d %q", st, code, msg)
	}

	gr := schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}
	st, code, msg = ClassifyMetricsError(apierrors.NewForbidden(gr, "", fmt.Errorf("denied")))
	if st != http.StatusForbidden || code != 403 {
		t.Fatalf("forbidden: got %d %d %q", st, code, msg)
	}

	st, code, msg = ClassifyMetricsError(apierrors.NewNotFound(gr, "x"))
	if st != http.StatusNotFound || code != 404 {
		t.Fatalf("notfound: got %d %d %q", st, code, msg)
	}

	st, code, msg = ClassifyMetricsError(fmt.Errorf(`no matches for kind "PodMetrics" in version "metrics.k8s.io/v1beta1"`))
	if st != http.StatusNotFound || code != 404 {
		t.Fatalf("nomatch: got %d %d %q", st, code, msg)
	}

	st, code, msg = ClassifyMetricsError(fmt.Errorf("connection refused"))
	if st != http.StatusBadGateway || code != 502 {
		t.Fatalf("other: got %d %d %q", st, code, msg)
	}
}
