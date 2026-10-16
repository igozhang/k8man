package store

import (
	"path/filepath"
	"testing"
	"time"

	"gitee.com/k8man/internal/model"
)

func TestUpdateClusterProbeKeepsKubeconfig(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	cl := &model.Cluster{
		ID: "c1", Name: "prod", Description: "keep", Kubeconfig: "secret-kubeconfig",
		PermType: "cluster", Namespaces: []string{"a"}, Server: "https://old", Status: "error",
		LastError: "down", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateCluster(cl); err != nil {
		t.Fatal(err)
	}
	cl.Name = "changed"
	cl.Kubeconfig = "overwritten"
	cl.Description = "overwritten"
	cl.Status = "error"
	cl.LastError = "still down"
	cl.UpdatedAt = now.Add(time.Hour)
	if err := st.UpdateClusterProbe(cl, false); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCluster("c1", true)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Kubeconfig != "secret-kubeconfig" || got.Name != "prod" || got.Description != "keep" {
		t.Fatalf("probe write changed identity: %+v", got)
	}
	if got.Status != "error" || got.LastError != "still down" || !got.UpdatedAt.Equal(now) {
		t.Fatalf("status update: %+v", got)
	}
	if len(got.Namespaces) != 1 || got.Namespaces[0] != "a" || got.Server != "https://old" {
		t.Fatalf("failed probe should keep the previous inventory: %+v", got)
	}
	cl.Status = "ok"
	cl.Server = "https://new"
	cl.Namespaces = []string{"b"}
	cl.PermType = "project"
	cl.KubeVersion = "v1.28.0"
	cl.FrontProxy = ""
	cl.LastError = ""
	if err := st.UpdateClusterProbe(cl, true); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetCluster("c1", true)
	if got.Kubeconfig != "secret-kubeconfig" || got.Name != "prod" || got.Server != "https://new" || got.Status != "ok" {
		t.Fatalf("successful probe: %+v", got)
	}
	if len(got.Namespaces) != 1 || got.Namespaces[0] != "b" || !got.UpdatedAt.Equal(now) {
		t.Fatalf("inventory update: %+v", got)
	}
}

func TestInspectionCRUD(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	cl := &model.Cluster{
		ID: "c1", Name: "prod", Kubeconfig: "apiVersion: v1", PermType: "cluster",
		Namespaces: []string{}, Status: "ok", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateCluster(cl); err != nil {
		t.Fatal(err)
	}
	row := &InspectionRow{
		ClusterID: "c1", CheckedAt: now, NextDue: now.Add(2 * time.Hour),
		ElapsedMs: 12, ProbeStatus: "ok", Critical: 1, Warning: 2,
		Summary: "1 严重 / 2 警告", ReportJSON: `{"issues":[]}`,
	}
	if err := st.UpsertInspection(row); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetInspection("c1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %+v", err, got)
	}
	if got.Critical != 1 || got.Summary != row.Summary || got.ProbeStatus != "ok" {
		t.Fatalf("got %+v", got)
	}
	row.Critical = 3
	row.Summary = "3 严重"
	if err := st.UpsertInspection(row); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetInspection("c1")
	if got.Critical != 3 {
		t.Fatalf("upsert overwrite: %+v", got)
	}
	list, err := st.ListInspections()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if err := st.DeleteInspection("c1"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetInspection("c1")
	if err != nil || got != nil {
		t.Fatalf("deleted: %v %+v", err, got)
	}
}
