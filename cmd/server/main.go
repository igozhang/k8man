package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"

	"gitee.com/k8man/internal/api"
	"gitee.com/k8man/internal/config"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/store"
)

//go:embed all:webdist
var webEmbed embed.FS

var version = "0.1.0"

func main() {
	cfg := config.Load()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var webFS fs.FS
	sub, err := fs.Sub(webEmbed, "webdist")
	if err == nil {
		if f, err := sub.Open("index.html"); err == nil {
			_ = f.Close()
			webFS = sub
		}
	}

	r := api.NewRouter(api.Deps{
		Store:   st,
		Pool:    kube.NewPool(),
		WebFS:   webFS,
		Version: version,
		UI: api.UIConfig{
			EtcDir: cfg.EtcDir,
		},
	})

	ui := config.LoadUI(cfg.EtcDir)
	bannerNote := "default"
	if ui.BannerPath != "" {
		bannerNote = ui.BannerPath
	}
	log.Printf("k8man %s listening on %s (db=%s etc=%s banner=%s)", version, cfg.Addr, cfg.DBPath, cfg.EtcDir, bannerNote)
	if err := r.Run(cfg.Addr); err != nil && err != http.ErrServerClosed {
		log.Println(err)
		os.Exit(1)
	}
}
