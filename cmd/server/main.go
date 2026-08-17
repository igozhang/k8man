package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"gitee.com/k8man/internal/api"
	"gitee.com/k8man/internal/config"
	"gitee.com/k8man/internal/hostsfile"
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

	hostsResolver := kube.NewHostsResolver()
	pool := kube.NewPool(hostsResolver)
	hostsFile := hostsfile.New(cfg.EtcDir, hostsResolver, pool.InvalidateAll)
	if err := hostsFile.Ensure(); err != nil {
		log.Fatalf("ensure hosts file: %v", err)
	}
	// One-time migrate from legacy SQLite host_aliases if etc/hosts has no entries yet.
	if m, err := st.HostAliasMap(); err == nil && len(m) > 0 {
		if err := hostsFile.SeedFromMap(m); err != nil {
			log.Printf("migrate host aliases to etc/hosts: %v", err)
		}
	}
	if err := hostsFile.Reload(); err != nil {
		log.Fatalf("load hosts file: %v", err)
	}
	go hostsFile.Watch(context.Background(), 2*time.Second)

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
		Pool:    pool,
		Hosts:   hostsFile,
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
	log.Printf("k8man %s listening on %s (db=%s etc=%s hosts=%s banner=%s)",
		version, cfg.Addr, cfg.DBPath, cfg.EtcDir, hostsFile.Path(), bannerNote)
	if err := r.Run(cfg.Addr); err != nil && err != http.ErrServerClosed {
		log.Println(err)
		os.Exit(1)
	}
}
