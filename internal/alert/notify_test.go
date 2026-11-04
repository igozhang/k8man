package alert

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

func TestTitleLooksLikeTest(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"义乌-test", true},
		{"义乌-TEST", true},
		{"测试环境", true},
		{"prod-测试", true},
		{"义乌-MOM", false},
		{"prod", false},
		{"", false},
		{"contest", true}, // substring match by design
	}
	for _, tc := range cases {
		if got := TitleLooksLikeTest(tc.name); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.name, got, tc.want)
		}
	}
	if MuteMediaForCluster("义乌-test", true) != true {
		t.Fatal("mute on + test title")
	}
	if MuteMediaForCluster("义乌-test", false) {
		t.Fatal("mute off should not mute")
	}
	if MuteMediaForCluster("prod", true) {
		t.Fatal("prod should not mute")
	}
}

func TestNotifyMuteTestClusterSkipsMedia(t *testing.T) {
	var bodies int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	st := openAlertStore(t)
	defer st.Close()
	now := time.Date(2026, 11, 4, 2, 0, 0, 0, time.UTC)
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	n := New(st)
	n.now = func() time.Time { return now }
	n.Client = &http.Client{Transport: rewriteHost{base: srv.URL, rt: http.DefaultTransport}}
	issue := kube.Issue{Severity: "critical", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "CrashLoopBackOff"}

	n.Notify(&model.Cluster{ID: "c-test", Name: "义乌-test"}, []kube.Issue{issue}, RecoverGate{}, 0)
	if bodies != 0 {
		t.Fatalf("muted test cluster posted %d", bodies)
	}
	left, err := st.ListAlertSent("c-test")
	if err != nil || len(left) != 0 {
		t.Fatalf("muted must not write alert_sent: %v %+v", err, left)
	}

	if err := st.SetAlertMuteTestClusters(false); err != nil {
		t.Fatal(err)
	}
	n.Notify(&model.Cluster{ID: "c-test", Name: "义乌-test"}, []kube.Issue{issue}, RecoverGate{}, 0)
	if bodies != 1 {
		t.Fatalf("mute off should post, got %d", bodies)
	}
}

func TestIsAlertable(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		it   kube.Issue
		want bool
	}{
		{
			name: "critical always",
			it:   kube.Issue{Severity: "critical", SinceAt: now.Add(-5 * time.Hour).Format(time.RFC3339)},
			want: true,
		},
		{
			name: "error within 2h",
			it:   kube.Issue{Severity: "error", SinceAt: now.Add(-30 * time.Minute).Format(time.RFC3339)},
			want: true,
		},
		{
			name: "error older than 2h",
			it:   kube.Issue{Severity: "error", SinceAt: now.Add(-3 * time.Hour).Format(time.RFC3339)},
			want: false,
		},
		{
			name: "error without sinceAt",
			it:   kube.Issue{Severity: "error"},
			want: true,
		},
		{
			name: "warning never",
			it:   kube.Issue{Severity: "warning", SinceAt: now.Add(-1 * time.Minute).Format(time.RFC3339)},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAlertable(tc.it, now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestValidateWebhookURL(t *testing.T) {
	ok := "https://open.feishu.cn/open-apis/bot/v2/hook/e52319a0-09dc-40f1-8132-73d7df06af83"
	if err := ValidateWebhookURL(ok); err != nil {
		t.Fatalf("want ok: %v", err)
	}
	bads := []string{
		"",
		"http://open.feishu.cn/open-apis/bot/v2/hook/x",
		"https://evil.example/open-apis/bot/v2/hook/x",
		"https://open.feishu.cn/other",
	}
	for _, u := range bads {
		if err := ValidateWebhookURL(u); err == nil {
			t.Fatalf("want error for %q", u)
		}
	}
}

func TestIssueFingerprint(t *testing.T) {
	a := kube.Issue{Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "CrashLoopBackOff", Severity: "critical"}
	b := a
	b.Severity = "error"
	if IssueFingerprint(a) != IssueFingerprint(b) {
		t.Fatal("fingerprint should ignore severity")
	}
	pull := kube.Issue{Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "ErrImagePull"}
	backoff := pull
	backoff.Reason = "ImagePullBackOff"
	if IssueFingerprint(pull) != IssueFingerprint(backoff) {
		t.Fatal("image pull reasons should be one incident")
	}
	if IssueFingerprint(pull) == IssueFingerprint(a) {
		t.Fatal("image pull must not match crashloop")
	}
	oom := kube.Issue{Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "OOMKilled"}
	if IssueFingerprint(oom) == IssueFingerprint(a) {
		t.Fatal("oom and crashloop are different incidents")
	}
	down := kube.Issue{Category: "node", Kind: "Node", Name: "n1", Reason: "NotReady"}
	unknown := down
	unknown.Reason = "Unknown"
	if IssueFingerprint(down) == IssueFingerprint(unknown) {
		t.Fatal("notready and unknown are different incidents")
	}
}

func TestFormatMessage(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	msg := FormatMessage("独资-PRO", []kube.Issue{{
		Severity:  "critical",
		Category:  "workload",
		Kind:      "StatefulSet",
		Namespace: "cattle-monitoring-system",
		Name:      "prometheus-rancher-monitoring-prometheus",
		Reason:    "NotReady",
		Message:   "ready 0/1",
		Since:     "2h",
		Evidence: []string{
			"replicas desired=1 ready=0 current=0 updated=0",
			"Ready=False MinimumReplicasUnavailable: ...",
		},
	}}, now)
	wantParts := []string{
		"【k8man 告警】",
		"独资-PRO",
		"sts cattle-monitoring-system/prometheus-rancher-monitoring-prometheus",
		"NotReady",
		"Ready 0/1",
		"已持续 2h",
		"副本 desired=1 ready=0",
	}
	if !containsAll(msg, wantParts...) {
		t.Fatalf("unexpected message:\n%s", msg)
	}
}

func TestFormatMessageNFSLegacyMessageEnriched(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	msg := FormatMessage("prod", []kube.Issue{{
		Severity: "critical",
		Category: "storage",
		Kind:     "StorageClass",
		Name:     "nfs-sc",
		Reason:   "NFSHighUsage",
		Message:  "NFS 使用率 96%（763.10Gi / 797.85Gi），超过 80%",
		NodeIP:   "10.1.2.3",
		Evidence: []string{
			"server=10.1.2.3",
			"path=/export",
		},
	}}, now)
	wantParts := []string{
		"10.1.2.3:/export",
		"NFS服务器 10.1.2.3",
	}
	if !containsAll(msg, wantParts...) {
		t.Fatalf("legacy NFS alert missing server:\n%s", msg)
	}
}

func TestFormatMessageNFSIncludesServer(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	msg := FormatMessage("prod", []kube.Issue{{
		Severity: "critical",
		Category: "storage",
		Kind:     "StorageClass",
		Name:     "nfs-sc",
		Reason:   "NFSHighUsage",
		Message:  "NFS 10.1.2.3:/export 使用率 96%（763.10Gi / 797.85Gi），超过 80%",
		NodeIP:   "10.1.2.3",
		Evidence: []string{
			"server=10.1.2.3",
			"path=/export",
			"usage=96.0%",
			"sc=nfs-sc",
		},
	}}, now)
	wantParts := []string{
		"sc nfs-sc",
		"NFSHighUsage",
		"10.1.2.3:/export",
		"NFS服务器 10.1.2.3",
	}
	if !containsAll(msg, wantParts...) {
		t.Fatalf("unexpected NFS alert:\n%s", msg)
	}
}

func TestControllerLabel(t *testing.T) {
	cases := map[string]string{
		"Deployment":   "deploy",
		"StatefulSet":  "sts",
		"DaemonSet":    "ds",
		"Pod":          "pod",
		"Node":         "node",
		"StorageClass": "sc",
	}
	for in, want := range cases {
		if got := controllerLabel(in); got != want {
			t.Fatalf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestClassifyAlertsRecovery(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	fp := "pod|Pod|ns|web|CrashLoopBackOff"
	nfs := "storage|StorageClass||nfs-sc|NFSHighUsage"
	sent := map[string]store.AlertSentRow{
		fp:  {Fingerprint: fp, Severity: "critical", Detail: "1. [严重] pod ns/web"},
		nfs: {Fingerprint: nfs, Severity: "critical", Detail: "1. [严重] sc nfs-sc"},
	}
	pending, recovered := classifyAlerts(nil, sent, now, RecoverGate{OK: true})
	if len(pending) != 0 || len(recovered) != 2 {
		t.Fatalf("pending=%d recovered=%d", len(pending), len(recovered))
	}
	_, recovered = classifyAlerts(nil, sent, now, RecoverGate{})
	if len(recovered) != 0 {
		t.Fatal("incomplete scan must not recover")
	}
	_, recovered = classifyAlerts(nil, sent, now, RecoverGate{OK: true, HoldNFS: true})
	if len(recovered) != 1 || recovered[0].Fingerprint != fp {
		t.Fatalf("nfs hold recovered=%+v", recovered)
	}
	uc := "kubeconfig|Kubeconfig||电白-TEST|Unreachable"
	sentUC := map[string]store.AlertSentRow{
		fp: {Fingerprint: fp, Severity: "critical", Detail: "pod"},
		uc: {Fingerprint: uc, Severity: "critical", Detail: "uc"},
	}
	_, recovered = classifyAlerts(nil, sentUC, now, RecoverGate{OK: true, HoldUnreachable: true})
	if len(recovered) != 1 || recovered[0].Fingerprint != fp {
		t.Fatalf("unreachable hold recovered=%+v", recovered)
	}
	still := []kube.Issue{{
		Severity: "warning", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "CrashLoopBackOff",
	}}
	pending, recovered = classifyAlerts(still, sent, now, RecoverGate{OK: true})
	if len(pending) != 0 || len(recovered) != 1 || recovered[0].Fingerprint != nfs {
		t.Fatalf("decayed-but-present pending=%d recovered=%+v", len(pending), recovered)
	}
	escalated := []kube.Issue{{
		Severity: "critical", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "CrashLoopBackOff",
	}}
	sent[fp] = store.AlertSentRow{Fingerprint: fp, Severity: "error"}
	pending, _ = classifyAlerts(escalated, sent, now, RecoverGate{OK: true})
	if len(pending) != 1 {
		t.Fatal("error to critical should re-alert")
	}
}

func TestClassifyScopeClosesOnlyRechecked(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	pod := "pod|Pod|ns|web|CrashLoopBackOff"
	nfs := "storage|StorageClass||nfs-sc|NFSHighUsage"
	large := "storage|Pod|ns|big|LargeEphemeral"
	img := "pod|Pod|ns|web|ErrImagePull"
	sent := map[string]store.AlertSentRow{
		pod:   {Fingerprint: pod, Severity: "critical", Detail: "pod"},
		nfs:   {Fingerprint: nfs, Severity: "critical", Detail: "nfs"},
		large: {Fingerprint: large, Severity: "critical", Detail: "large"},
		img:   {Fingerprint: img, Severity: "critical", Detail: "img"},
	}
	_, recovered := classifyAlerts(nil, sent, now, RecoverGate{OK: true, Scope: map[string]struct{}{}})
	if len(recovered) != 0 {
		t.Fatalf("empty scope must not recover, got %+v", recovered)
	}
	scope := map[string]struct{}{canonicalFingerprint(pod): {}}
	_, recovered = classifyAlerts(nil, sent, now, RecoverGate{OK: true, Scope: scope})
	if len(recovered) != 1 || recovered[0].Fingerprint != pod {
		t.Fatalf("only the rechecked pod should recover, got %+v", recovered)
	}
	scope = map[string]struct{}{canonicalFingerprint(img): {}}
	_, recovered = classifyAlerts(nil, sent, now, RecoverGate{OK: true, Scope: scope})
	if len(recovered) != 1 || canonicalFingerprint(recovered[0].Fingerprint) != canonicalFingerprint(img) {
		t.Fatalf("image pull canonical scope should recover the stored label, got %+v", recovered)
	}
}

func TestClassifyImagePullIsOneIncident(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	errPull := "pod|Pod|ns|web|ErrImagePull"
	backoff := "pod|Pod|ns|web|ImagePullBackOff"
	sent := map[string]store.AlertSentRow{
		errPull: {Fingerprint: errPull, Severity: "critical", Detail: "err"},
		backoff: {Fingerprint: backoff, Severity: "critical", Detail: "backoff"},
	}
	still := []kube.Issue{{
		Severity: "critical", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "ImagePullBackOff",
	}}
	pending, recovered := classifyAlerts(still, sent, now, RecoverGate{OK: true})
	if len(pending) != 0 || len(recovered) != 0 {
		t.Fatalf("flip should be silent: pending=%d recovered=%d", len(pending), len(recovered))
	}
	pending, recovered = classifyAlerts(nil, sent, now, RecoverGate{OK: true})
	if len(pending) != 0 || len(recovered) != 1 {
		t.Fatalf("real clear: pending=%d recovered=%d", len(pending), len(recovered))
	}
	fps := fingerprintsToClear(sent, recovered)
	if len(fps) != 2 {
		t.Fatalf("both stored reasons should clear, got %v", fps)
	}
}

func TestClassifyDifferentReasonsStaySeparate(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	crash := "pod|Pod|ns|web|CrashLoopBackOff"
	sent := map[string]store.AlertSentRow{
		crash: {Fingerprint: crash, Severity: "critical", Detail: "crash"},
	}
	oom := []kube.Issue{{
		Severity: "critical", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "OOMKilled",
	}}
	pending, recovered := classifyAlerts(oom, sent, now, RecoverGate{OK: true})
	if len(pending) != 1 || len(recovered) != 1 {
		t.Fatalf("oom is not crashloop: pending=%d recovered=%d", len(pending), len(recovered))
	}
	notReady := []kube.Issue{{
		Severity: "critical", Category: "node", Kind: "Node", Name: "n1", Reason: "NotReady",
	}}
	unknown := "node|Node||n1|Unknown"
	pending, recovered = classifyAlerts(notReady, map[string]store.AlertSentRow{
		unknown: {Fingerprint: unknown, Severity: "critical", Detail: "unknown"},
	}, now, RecoverGate{OK: true})
	if len(pending) != 1 || len(recovered) != 1 {
		t.Fatalf("notready is not unknown: pending=%d recovered=%d", len(pending), len(recovered))
	}
}

func TestAlignImagePullKeepsOneRow(t *testing.T) {
	st := openAlertStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 20, 56, 38, 0, time.UTC)
	n := New(st)
	n.now = func() time.Time { return now }
	fp := "pod|Pod|cattle-fleet-local-system|fleet-agent|ErrImagePull"
	if err := st.UpsertAlertSent("c1", fp, "critical", "old", now); err != nil {
		t.Fatal(err)
	}
	n.notifyCluster(context.Background(), &model.Cluster{ID: "c1", Name: "义乌-MOM"}, []kube.Issue{{
		Severity: "critical", Category: "pod", Kind: "Pod",
		Namespace: "cattle-fleet-local-system", Name: "fleet-agent", Reason: "ImagePullBackOff",
	}}, RecoverGate{OK: true}, 0)
	left, err := st.ListAlertSent("c1")
	if err != nil || len(left) != 1 {
		t.Fatalf("sent: %v %+v", err, left)
	}
	if _, ok := left["pod|Pod|cattle-fleet-local-system|fleet-agent|ImagePull"]; !ok {
		t.Fatalf("want one image-pull row, got %+v", left)
	}
}

func TestRecoverGateFor(t *testing.T) {
	if RecoverGateFor("ok", nil).OK {
		t.Fatal("nil diagnose")
	}
	if RecoverGateFor("error", &kube.DiagnoseReport{}).OK {
		t.Fatal("probe failed")
	}
	if RecoverGateFor("ok", &kube.DiagnoseReport{Truncated: true}).OK {
		t.Fatal("truncated")
	}
	if RecoverGateFor("ok", &kube.DiagnoseReport{Errors: map[string]string{"nodes": "timeout"}}).OK {
		t.Fatal("category error")
	}
	g := RecoverGateFor("ok", &kube.DiagnoseReport{Scan: kube.DiagnoseScan{NFSSkipped: "no mount"}})
	if !g.OK || !g.HoldNFS || g.HoldLargePod {
		t.Fatalf("%+v", g)
	}
}

func TestFormatRecovery(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	msg := FormatRecovery("prod", []string{"1. [严重] pod ns/web\n   CrashLoopBackOff"}, now)
	if !containsAll(msg, "【k8man 恢复】", "prod", "以下 1 条已恢复", "pod ns/web", "CrashLoopBackOff") {
		t.Fatalf("%s", msg)
	}
}

func TestNotifyClusterRecoveryAndIncomplete(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	st := openAlertStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	fp := "pod|Pod|ns|web|CrashLoopBackOff"
	if err := st.UpsertAlertSent("c1", fp, "critical", "1. [严重] pod ns/web\n", now); err != nil {
		t.Fatal(err)
	}
	n := New(st)
	n.now = func() time.Time { return now }
	n.Client = &http.Client{Transport: rewriteHost{base: srv.URL, rt: http.DefaultTransport}}
	cl := &model.Cluster{ID: "c1", Name: "prod"}

	n.notifyCluster(context.Background(), cl, nil, RecoverGate{}, 0)
	if len(bodies) != 0 {
		t.Fatalf("incomplete scan posted %d", len(bodies))
	}
	left, err := st.ListAlertSent("c1")
	if err != nil || len(left) != 1 {
		t.Fatalf("sent should stay: %v %+v", err, left)
	}

	n.notifyCluster(context.Background(), cl, nil, RecoverGate{OK: true}, 0)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "【k8man 恢复】") {
		t.Fatalf("bodies=%v", bodies)
	}
	left, err = st.ListAlertSent("c1")
	if err != nil || len(left) != 0 {
		t.Fatalf("sent should clear: %v %+v", err, left)
	}
}

func TestNotifySameClusterIsSerial(t *testing.T) {
	var mu sync.Mutex
	var inFlight, maxIn int
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxIn {
			maxIn = inFlight
		}
		mu.Unlock()
		started <- struct{}{}
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	st := openAlertStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	n := New(st)
	n.now = func() time.Time { return now }
	n.Client = &http.Client{Transport: rewriteHost{base: srv.URL, rt: http.DefaultTransport}}
	cl := &model.Cluster{ID: "c1", Name: "prod"}
	issue := kube.Issue{Severity: "critical", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "CrashLoopBackOff"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n.Notify(cl, []kube.Issue{issue}, RecoverGate{}, 0)
	}()
	go func() {
		defer wg.Done()
		n.Notify(cl, []kube.Issue{issue}, RecoverGate{}, 0)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("webhook did not start")
	}
	select {
	case <-started:
		t.Fatal("second webhook entered before the first finished")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if maxIn != 1 {
		t.Fatalf("max in flight %d", maxIn)
	}
	left, err := st.ListAlertSent("c1")
	if err != nil || len(left) != 1 {
		t.Fatalf("one fingerprint: %v %+v", err, left)
	}
}

func TestStaleAlertRoundIsDropped(t *testing.T) {
	var bodies int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	st := openAlertStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	n := New(st)
	n.now = func() time.Time { return now }
	n.Client = &http.Client{Transport: rewriteHost{base: srv.URL, rt: http.DefaultTransport}}
	cl := &model.Cluster{ID: "c1", Name: "prod"}
	issue := kube.Issue{Severity: "critical", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "CrashLoopBackOff"}
	first := n.NextRound(cl.ID)
	second := n.NextRound(cl.ID)
	n.Notify(cl, []kube.Issue{issue}, RecoverGate{}, first)
	if bodies != 0 {
		t.Fatalf("stale round posted %d", bodies)
	}
	n.Notify(cl, []kube.Issue{issue}, RecoverGate{}, second)
	if bodies != 1 {
		t.Fatalf("latest round bodies=%d", bodies)
	}
}

func TestCurrentRoundDoesNotBump(t *testing.T) {
	n := New(nil)
	if n.CurrentRound("c1") != 0 {
		t.Fatal("no inspect yet")
	}
	if got := n.NextRound("c1"); got != 1 || n.CurrentRound("c1") != 1 {
		t.Fatalf("latest=%d current=%d", got, n.CurrentRound("c1"))
	}
	if n.CurrentRound("c1") != 1 {
		t.Fatal("reading the current round must not open a new one")
	}
}

type rewriteHost struct {
	base string
	rt   http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(r.base)
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	req.Host = u.Host
	return r.rt.RoundTrip(req)
}

func openAlertStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
