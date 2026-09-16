package kube

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	t.Run("unique fuseim nfs-client", func(t *testing.T) {
		sc := storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: "managed-nfs-storage"},
			Provisioner: "fuseim.pri/ifs",
		}
		pick, skip := pickNFSStorageClass([]storagev1.StorageClass{sc})
		if skip != "" || pick.Source != "unique" || pick.Name != "managed-nfs-storage" {
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
	if !isNFSSC(storagev1.StorageClass{Provisioner: "fuseim.pri/ifs"}) {
		t.Fatal("expected fuseim.pri/ifs (nfs-client-provisioner)")
	}
	if !isNFSSC(storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "managed-nfs-storage"},
		Provisioner: "fuseim.pri/ifs",
	}) {
		t.Fatal("expected managed-nfs-storage + fuseim")
	}
	if !isNFSSC(storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "managed-nfs-storage"},
		Provisioner: "example.com/custom",
	}) {
		t.Fatal("SC name containing nfs should count for unknown provisioner")
	}
	if isNFSSC(storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "managed-nfs-storage"},
		Provisioner: "rancher.io/local-path",
	}) {
		t.Fatal("name hint must not override local-path")
	}
	if isNFSSC(storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "non-nfs-backup"},
		Provisioner: "example.com/custom",
	}) {
		t.Fatal("non-nfs name should not count")
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

func TestPodEphemeralUsagesFromSummaries(t *testing.T) {
	small := uint64(100)
	mid := uint64(2 * 1024 * 1024 * 1024)
	big := uint64(diagnoseLargePodBytes + 10)
	sum := map[string]nodeStatsSummary{
		"n1": {
			Pods: []podStatsSummary{
				{
					PodRef:           podStatsRef{Namespace: "ns-a", Name: "tiny"},
					EphemeralStorage: &fsStats{UsedBytes: &small},
				},
				{
					PodRef:           podStatsRef{Namespace: "ns-a", Name: "big"},
					EphemeralStorage: &fsStats{UsedBytes: &big},
				},
				{
					PodRef:           podStatsRef{Namespace: "other", Name: "skip"},
					EphemeralStorage: &fsStats{UsedBytes: &mid},
				},
			},
		},
		"n2": {
			Pods: []podStatsSummary{
				{
					PodRef:           podStatsRef{Namespace: "ns-a", Name: "mid"},
					EphemeralStorage: &fsStats{UsedBytes: &mid},
				},
			},
		},
	}
	got := podEphemeralUsagesFromSummaries(sum, "ns-a")
	if len(got) != 3 {
		t.Fatalf("len=%d want 3: %+v", len(got), got)
	}
	if got[0].Name != "big" || got[0].UsedBytes != int64(big) || got[0].Node != "n1" {
		t.Fatalf("first=%+v", got[0])
	}
	if got[1].Name != "mid" || got[2].Name != "tiny" {
		t.Fatalf("order=%+v", got)
	}
	all := podEphemeralUsagesFromSummaries(sum, "")
	if len(all) != 4 {
		t.Fatalf("all len=%d", len(all))
	}
	empty := podEphemeralUsagesFromSummaries(nil, "ns-a")
	if len(empty) != 0 {
		t.Fatalf("nil summaries should be empty, got %+v", empty)
	}
}

func TestNodeNamesForSummarySkipsUnscheduled(t *testing.T) {
	pods := []corev1.Pod{
		{Spec: corev1.PodSpec{NodeName: ""}},
		{Spec: corev1.PodSpec{NodeName: "n1"}},
		{Spec: corev1.PodSpec{NodeName: "n1"}},
		{Spec: corev1.PodSpec{NodeName: "n2"}},
	}
	names := nodeNamesForSummary(pods)
	if len(names) != 2 || names[0] != "n1" || names[1] != "n2" {
		t.Fatalf("names=%v", names)
	}
	if len(nodeNamesForSummary(nil)) != 0 {
		t.Fatal("expected empty")
	}
}

func TestPodNonPersistentUsedBytesFallbackContainers(t *testing.T) {
	// Older kubelet: no pod-level ephemeral-storage; use rootfs+logs+non-PVC.
	rootfs := uint64(6 * 1024 * 1024 * 1024)
	logs := uint64(2 * 1024 * 1024 * 1024)
	pvcUsed := uint64(20 * 1024 * 1024 * 1024)
	empty := uint64(1 * 1024 * 1024 * 1024)
	p := podStatsSummary{
		PodRef: podStatsRef{Namespace: "ns", Name: "app"},
		Containers: []containerStatsSummary{
			{Name: "c1", Rootfs: &fsStats{UsedBytes: &rootfs}, Logs: &fsStats{UsedBytes: &logs}},
		},
		VolumeStats: []volumeStatsSummary{
			{Name: "data", UsedBytes: &pvcUsed, PVCRef: &pvcRef{Namespace: "ns", Name: "pvc1"}},
			{Name: "tmp", UsedBytes: &empty},
		},
	}
	used, ok := podNonPersistentUsedBytes(p)
	if !ok {
		t.Fatal("expected ok")
	}
	want := int64(rootfs + logs + empty)
	if used != want {
		t.Fatalf("used=%d want=%d", used, want)
	}
	issues := largePodIssuesFromSummaries(map[string]nodeStatsSummary{"n1": {Pods: []podStatsSummary{p}}})
	if len(issues) != 1 || issues[0].Name != "app" {
		t.Fatalf("issues=%+v", issues)
	}
}

func TestPodNonPersistentUsedBytesPrefersPodEphemeral(t *testing.T) {
	small := uint64(100)
	large := uint64(diagnoseLargePodBytes + 10)
	p := podStatsSummary{
		PodRef:           podStatsRef{Namespace: "ns", Name: "app"},
		EphemeralStorage: &fsStats{UsedBytes: &large},
		Containers: []containerStatsSummary{
			{Name: "c1", Rootfs: &fsStats{UsedBytes: &small}},
		},
	}
	used, ok := podNonPersistentUsedBytes(p)
	if !ok || used != int64(large) {
		t.Fatalf("used=%d ok=%v", used, ok)
	}
}

func TestPodNonPersistentUsedBytesZeroEphemeralFallsBack(t *testing.T) {
	zero := uint64(0)
	rootfs := uint64(6 * 1024 * 1024 * 1024)
	p := podStatsSummary{
		PodRef:           podStatsRef{Namespace: "ns", Name: "app"},
		EphemeralStorage: &fsStats{UsedBytes: &zero},
		Containers: []containerStatsSummary{
			{Name: "c1", Rootfs: &fsStats{UsedBytes: &rootfs}},
		},
	}
	used, ok := podNonPersistentUsedBytes(p)
	if !ok || used != int64(rootfs) {
		t.Fatalf("used=%d ok=%v", used, ok)
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
