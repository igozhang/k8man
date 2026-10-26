package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gitee.com/k8man/internal/model"
)

func TestListDocsPage(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 25; i++ {
		d := &model.Doc{
			ID:        fmt.Sprintf("d%d", i),
			Title:     fmt.Sprintf("doc-%02d", i),
			Category:  "general",
			Summary:   "s",
			Pinned:    i == 25,
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := st.CreateDoc(d); err != nil {
			t.Fatal(err)
		}
	}
	// extra category so filter is exercised
	if err := st.CreateDoc(&model.Doc{
		ID: "x1", Title: "other", Category: "k8s", Summary: "s",
		CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}

	list, total, page, size, err := st.ListDocsPage("general", "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 || page != 1 || size != 20 || len(list) != 20 {
		t.Fatalf("page1: total=%d page=%d size=%d n=%d", total, page, size, len(list))
	}
	if !list[0].Pinned || list[0].ID != "d25" {
		t.Fatalf("pinned should lead: %+v", list[0])
	}

	list, total, page, size, err = st.ListDocsPage("general", "", 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 || page != 2 || size != 20 || len(list) != 5 {
		t.Fatalf("page2: total=%d page=%d size=%d n=%d", total, page, size, len(list))
	}

	list, total, page, size, err = st.ListDocsPage("general", "", 99, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 || page != 2 || size != 20 || len(list) != 5 {
		t.Fatalf("clamp: total=%d page=%d size=%d n=%d", total, page, size, len(list))
	}

	// pageSize above max is clamped; page below 1 becomes 1
	list, total, page, size, err = st.ListDocsPage("general", "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 || page != 1 || size != MaxDocPageSize || len(list) != 25 {
		t.Fatalf("normalize: total=%d page=%d size=%d n=%d", total, page, size, len(list))
	}

	list, total, page, size, err = st.ListDocsPage("", "doc-01", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || page != 1 || size != DefaultDocPageSize || len(list) != 1 || list[0].ID != "d1" {
		t.Fatalf("search: total=%d page=%d size=%d list=%+v", total, page, size, list)
	}

	list, total, page, size, err = st.ListDocsPage("missing", "", 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || page != 1 || size != MaxDocPageSize || len(list) != 0 {
		t.Fatalf("empty: total=%d page=%d size=%d n=%d", total, page, size, len(list))
	}
}
