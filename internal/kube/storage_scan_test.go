package kube

import (
	"context"
	"fmt"
	"testing"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPickNFSStorageClass(t *testing.T) {
	nfs := storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "nfs-sc"},
		Provisioner: "nfs.csi.k8s.io",
	}
	local := storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "local"},
		Provisioner: "kubernetes.io/no-provisioner",
	}
	defNFS := nfs
	defNFS.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	defLocal := local
	defLocal.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}

	t.Run("default nfs", func(t *testing.T) {
		pick, skip := pickNFSStorageClass([]storagev1.StorageClass{local, defNFS})
		if skip != "" || pick.Name != "nfs-sc" || pick.Source != "default" {
			t.Fatalf("got %+v skip=%q", pick, skip)
		}
	})
	t.Run("default not nfs", func(t *testing.T) {
		_, skip := pickNFSStorageClass([]storagev1.StorageClass{nfs, defLocal})
		if skip != "default SC is not NFS" {
			t.Fatalf("skip=%q", skip)
		}
	})
	t.Run("unique nfs without default", func(t *testing.T) {
		pick, skip := pickNFSStorageClass([]storagev1.StorageClass{nfs})
		if skip != "" || pick.Source != "unique" || pick.Name != "nfs-sc" {
			t.Fatalf("got %+v skip=%q", pick, skip)
		}
	})
	t.Run("unique not nfs", func(t *testing.T) {
		_, skip := pickNFSStorageClass([]storagev1.StorageClass{local})
		if skip != "unique SC is not NFS" {
			t.Fatalf("skip=%q", skip)
		}
	})
	t.Run("multiple no default", func(t *testing.T) {
		_, skip := pickNFSStorageClass([]storagev1.StorageClass{nfs, local})
		if skip != "no default SC and multiple SCs" {
			t.Fatalf("skip=%q", skip)
		}
	})
	t.Run("empty", func(t *testing.T) {
		_, skip := pickNFSStorageClass(nil)
		if skip != "no StorageClass" {
			t.Fatalf("skip=%q", skip)
		}
	})
}

func TestIsNFSSC(t *testing.T) {
	if !isNFSSC(storagev1.StorageClass{Provisioner: "cluster.local/nfs-subdir-external-provisioner"}) {
		t.Fatal("expected nfs provisioner")
	}
	if isNFSSC(storagev1.StorageClass{Provisioner: "rancher.io/local-path"}) {
		t.Fatal("local-path is not nfs")
	}
}

func TestParseDFPOutput(t *testing.T) {
	out := `Filesystem     1024-blocks      Used Available Capacity Mounted on
192.168.1.1:/export  104857600  83886080  20971520      80% /data
`
	used, cap, ok := parseDFPOutput(out)
	if !ok {
		t.Fatal("parse failed")
	}
	if cap != 104857600*1024 || used != 83886080*1024 {
		t.Fatalf("used=%d cap=%d", used, cap)
	}
}

func TestLargePodIssuesFromSummaries(t *testing.T) {
	under := uint64(diagnoseLargePodBytes)
	over := uint64(diagnoseLargePodBytes + 1)
	sum := map[string]nodeStatsSummary{
		"n1": {
			Pods: []podStatsSummary{
				{
					PodRef:           podStatsRef{Namespace: "ns", Name: "small"},
					EphemeralStorage: &fsStats{UsedBytes: &under},
				},
				{
					PodRef:           podStatsRef{Namespace: "ns", Name: "big"},
					EphemeralStorage: &fsStats{UsedBytes: &over},
				},
			},
		},
	}
	got := largePodIssuesFromSummaries(sum)
	if len(got) != 1 || got[0].Name != "big" || got[0].Reason != "LargeEphemeral" || got[0].Severity != sevWarning {
		t.Fatalf("got %+v", got)
	}
}

func TestLargePodCountIndependentOfIssueCap(t *testing.T) {
	over := uint64(diagnoseLargePodBytes + 1)
	sum := map[string]nodeStatsSummary{"n1": {Pods: make([]podStatsSummary, diagnoseLargePodIssueCap+3)}}
	for i := range sum["n1"].Pods {
		u := over
		sum["n1"].Pods[i] = podStatsSummary{
			PodRef:           podStatsRef{Namespace: "ns", Name: fmt.Sprintf("p%02d", i)},
			EphemeralStorage: &fsStats{UsedBytes: &u},
		}
	}
	all := largePodIssuesFromSummaries(sum)
	if len(all) != diagnoseLargePodIssueCap+3 {
		t.Fatalf("count=%d", len(all))
	}
	capped := all
	if len(capped) > diagnoseLargePodIssueCap {
		capped = capped[:diagnoseLargePodIssueCap]
	}
	if len(capped) != diagnoseLargePodIssueCap || all[0].Name != "p00" {
		t.Fatalf("cap=%d first=%s", len(capped), all[0].Name)
	}
}

func TestStorageScanContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sctx, stop, skip := storageScanContext(ctx)
	if skip != "" || sctx == nil {
		t.Fatalf("skip=%q", skip)
	}
	stop()

	done, doneCancel := context.WithCancel(context.Background())
	doneCancel()
	_, _, skip = storageScanContext(done)
	if skip == "" {
		t.Fatal("expected skip on cancelled parent")
	}

	short, shortCancel := context.WithTimeout(context.Background(), time.Second)
	defer shortCancel()
	_, _, skip = storageScanContext(short)
	if skip != "timeout budget exhausted" {
		t.Fatalf("short budget skip=%q", skip)
	}
}

func TestSeverityNeverDecayStorage(t *testing.T) {
	if !severityNeverDecay(Issue{Reason: "LargeEphemeral", Category: "storage"}) {
		t.Fatal("LargeEphemeral should not decay")
	}
	if !severityNeverDecay(Issue{Reason: "NFSHighUsage", Category: "storage"}) {
		t.Fatal("NFSHighUsage should not decay")
	}
}
