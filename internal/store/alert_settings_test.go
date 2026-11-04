package store

import (
	"path/filepath"
	"testing"
)

func TestAlertMuteTestClustersDefaultAndSet(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	got, err := st.GetAlertMuteTestClusters()
	if err != nil || !got {
		t.Fatalf("default want true, got %v err=%v", got, err)
	}
	if err := st.SetAlertMuteTestClusters(false); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAlertMuteTestClusters()
	if err != nil || got {
		t.Fatalf("after set false: got %v err=%v", got, err)
	}
	if err := st.SetAlertMuteTestClusters(true); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAlertMuteTestClusters()
	if err != nil || !got {
		t.Fatalf("after set true: got %v err=%v", got, err)
	}
}
