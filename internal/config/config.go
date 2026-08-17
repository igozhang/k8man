package config

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Addr    string
	DBPath  string
	DataDir string
	EtcDir  string

	Title      string
	Tagline    string
	BannerName string
}

type UISettings struct {
	Title      string
	Tagline    string
	BannerName string
	BannerPath string
}

// defaultConfBody: 头部为生效配置，尾部为说明与示例。
const defaultConfBody = `addr=:8080
data_dir=./data
title=k8s多集群运维工具

# =============================================================================
# 说明（以下均为注释，修改请改上方生效区）
# =============================================================================
# 格式：key=value，# 开头为注释。环境变量 K8MAN_* 优先级更高。
#
# | 配置项    | 环境变量         | 默认                    | 说明 |
# |-----------|------------------|-------------------------|------|
# | addr      | K8MAN_ADDR       | :8080                   | HTTP 监听 |
# | data_dir  | K8MAN_DATA_DIR   | ./data                  | 数据目录 |
# | db_path   | K8MAN_DB_PATH    | <data_dir>/k8man.db     | SQLite 路径，可省略 |
# | title     | （仅 conf）      | k8s多集群运维工具       | 头部居中标题 |
# | tagline   | （仅 conf）      | （空）                  | 标题下方副标题，可选 |
# | banner    | （仅 conf）      | （自动探测）            | etc 下图片文件名 |
#
# 头部 banner（可选，不使用外部站点图片）：
#   将自有图片放到 etc 目录即可启用；无文件则默认浅青面板风格。
#   探测顺序：banner= 指定名 → banner.jpg → .jpeg → .png → .webp → .gif
#
# ----- 示例 -----
# db_path=./data/k8man.db
# tagline=多集群运维
# banner=banner.jpg
#

`

func Load() Config {
	etcDir := getenv("K8MAN_ETC_DIR", "./etc")
	if err := EnsureDefaultConf(etcDir); err != nil {
		log.Printf("ensure etc conf: %v", err)
	}
	fileVals := readConfFile(filepath.Join(etcDir, "k8man.conf"))

	dataDir := firstNonEmpty(
		os.Getenv("K8MAN_DATA_DIR"),
		fileVals["data_dir"],
		"./data",
	)
	dbPath := firstNonEmpty(
		os.Getenv("K8MAN_DB_PATH"),
		fileVals["db_path"],
		filepath.Join(dataDir, "k8man.db"),
	)
	addr := firstNonEmpty(
		os.Getenv("K8MAN_ADDR"),
		fileVals["addr"],
		":8080",
	)
	ui := uiFromVals(fileVals)

	return Config{
		Addr:       addr,
		DBPath:     dbPath,
		DataDir:    dataDir,
		EtcDir:     etcDir,
		Title:      ui.Title,
		Tagline:    ui.Tagline,
		BannerName: ui.BannerName,
	}
}

// EnsureDefaultConf 在 etc 目录缺少有效 k8man.conf 时写入默认模板。
func EnsureDefaultConf(etcDir string) error {
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(etcDir, "k8man.conf")
	if confLooksValid(path) {
		return nil
	}
	return os.WriteFile(path, []byte(defaultConfBody), 0o644)
}

func confLooksValid(path string) bool {
	vals := readConfFile(path)
	// 至少能解析出一项有效 key 即视为已有配置（避免覆盖用户文件）
	return len(vals) > 0
}

func LoadUI(etcDir string) UISettings {
	_ = EnsureDefaultConf(etcDir)
	vals := readConfFile(filepath.Join(etcDir, "k8man.conf"))
	ui := uiFromVals(vals)
	ui.BannerPath = ResolveBanner(etcDir, ui.BannerName)
	return ui
}

func uiFromVals(fileVals map[string]string) UISettings {
	return UISettings{
		Title:      firstNonEmpty(fileVals["title"], "k8s多集群运维工具"),
		Tagline:    fileVals["tagline"],
		BannerName: fileVals["banner"],
	}
}

// ResolveBanner 仅使用 etc 目录下自有图片；无则返回空（前端用默认样式）。
func ResolveBanner(etcDir, configured string) string {
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates,
		"banner.jpg", "banner.jpeg", "banner.png", "banner.webp", "banner.gif",
	)
	seen := map[string]bool{}
	for _, name := range candidates {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		base := filepath.Base(name)
		if base == "." || base == ".." || seen[base] {
			continue
		}
		seen[base] = true
		p := filepath.Join(etcDir, base)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func readConfFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		val = strings.TrimSpace(val)
		if key != "" {
			out[key] = val
		}
	}
	return out
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
