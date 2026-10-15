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
	BannerName string
	IconName   string

	// InspectConcurrency is the base parallel patrol slots (default 4).
	InspectConcurrency int
	// InspectConcurrencyMax is the hard cap when backlog expands (default 8).
	InspectConcurrencyMax int
	// InspectRetentionDays drops inspection day dirs and auto daily docs older than this (default 365).
	InspectRetentionDays int
	// InspectRetentionMax caps how many auto daily reports / day dirs to keep (default 365).
	InspectRetentionMax int
}

type UISettings struct {
	Title      string
	Tagline    string
	BannerName string
	BannerPath string
	IconName   string
	IconPath   string
}

const defaultTitle = "k8s多集群运维"

// FixedTagline 页眉站名下方 slogan，写死不可配置。
const FixedTagline = "practice makes perfect"

// defaultConfBody: 头部为生效配置，尾部为说明与示例。
const defaultConfBody = `addr=:8080
data_dir=./data
title=k8s多集群运维

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
# | title     | （仅 conf）      | k8s多集群运维           | 页眉左侧品牌名；兼浏览器标签页标题 |
# | icon      | （仅 conf）      | （自动探测）            | etc 下站点 icon 文件名 |
# | banner    | （仅 conf）      | （自动探测）            | etc 下图片文件名 |
# | inspect_concurrency     | K8MAN_INSPECT_CONCURRENCY     | 4 | 巡检基础并发（有界并行） |
# | inspect_concurrency_max | K8MAN_INSPECT_CONCURRENCY_MAX | 8 | 积压时自动扩容上限（须 ≥ 基础并发，否则抬到 ≥ base） |
# | inspect_retention_days  | K8MAN_INSPECT_RETENTION_DAYS  | 365 | 巡检日目录与自动日报告保留天数 |
# | inspect_retention_max   | K8MAN_INSPECT_RETENTION_MAX   | 365 | 自动日报告 / 日目录最多保留份数，超出删最旧 |
# | hosts     | （文件）         | <etc>/hosts             | 私有解析，格式：IP hostname |
#
# 巡检文件落盘：<data_dir>/inspection/YYYY/MM/DD/HHMMSS/*.jsonl；日归档写 day.jsonl 并清轮次。
# 日目录与自动日报告（inspectallYYYYMMDD）默认保留 365 天且最多 365 份；置顶文档与人手导出的周/月/单集群报告不删。
# 清理在每天约 01:00 执行，进程启动时也会跑一次，与是否写出新报告、集群数量无关。
#
# 页眉 slogan 固定为 practice makes perfect，不可配置；双击可复制 igokey。
#
# 写操作鉴权（igokey，与站点方案相同）：
#   访问 /igokey 取得 8 位字母密钥（约 1 小时；剩余 >15 分钟刷新复用）。
#   在 /ops 验证后本会话可写；刷新页面须重新验证。密钥落在 <data_dir>/igokey-active.json。
#
# 站点 icon（可选）：
#   将 icon.jpg / icon.png 放到 etc 即作为 favicon 与页眉图标；
#   无文件则使用内置马里奥风格（红白蓝）数字 8。
#   探测顺序：icon= 指定名 → icon.jpg → .jpeg → .png
#
# 头部 banner（可选，不使用外部站点图片）：
#   将自有图片放到 etc 目录即可启用；无文件则默认浅青面板风格。
#   探测顺序：banner= 指定名 → banner.jpg → .jpeg → .png → .webp → .gif
#
# 私有 hosts（etc/hosts）：
#   10.10.10.10 igozhang.cn
#   可直接编辑该文件，或在页面 Hosts 管理中修改。
#
# 巡检（进程内，约 2 小时一轮）：
#   对每个已保存 kubeconfig 独立计时（完成后 ±6 分钟抖动）。
#   有界并发：默认同时 4 路；到期积压超过基础并发时自动扩容，上限默认 8。
#   启动节流：两次成功开始至少间隔约 2 秒；在途/忙集群跳过且不消耗间隔。
#   同一集群同时只跑一轮（后台、手动与恢复探针互斥）。先 Probe，成功再 Diagnose。
#   告警成功推送后进入恢复探针：约 3 分钟后首轮，之后每 5 分钟；满 6 小时改为每 15 分钟，直到恢复通知发出。
#   探针只巡检这一份 kubeconfig（落库、落盘，并重置约 2 小时的下一轮时间）。重启或重新启用 Webhook 后，未关闭的告警先等 3 分钟，再按 5 分钟。
#   单次巡检体超时约 45 秒；页面一键巡检 HTTP 约 90 秒。
#   进程退出或请求取消时不落库。结果在「监控」页底部；手动「一键巡检」走写接口。
#   改 kubeconfig / API Server / TLS 会丢掉该配置的旧巡检快照。
#
# ----- 示例 -----
# db_path=./data/k8man.db
# inspect_concurrency=4
# inspect_concurrency_max=8
# inspect_retention_days=365
# inspect_retention_max=365
# icon=icon.png
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

	baseConc := parsePositiveInt(firstNonEmpty(os.Getenv("K8MAN_INSPECT_CONCURRENCY"), fileVals["inspect_concurrency"]), 4)
	maxConc := parsePositiveInt(firstNonEmpty(os.Getenv("K8MAN_INSPECT_CONCURRENCY_MAX"), fileVals["inspect_concurrency_max"]), 8)
	if maxConc < baseConc {
		maxConc = baseConc
	}
	retainDays := parseBoundedInt(firstNonEmpty(os.Getenv("K8MAN_INSPECT_RETENTION_DAYS"), fileVals["inspect_retention_days"]), 365, 3650)
	retainMax := parseBoundedInt(firstNonEmpty(os.Getenv("K8MAN_INSPECT_RETENTION_MAX"), fileVals["inspect_retention_max"]), 365, 3650)

	return Config{
		Addr:                  addr,
		DBPath:                dbPath,
		DataDir:               dataDir,
		EtcDir:                etcDir,
		Title:                 ui.Title,
		BannerName:            ui.BannerName,
		IconName:              ui.IconName,
		InspectConcurrency:    baseConc,
		InspectConcurrencyMax: maxConc,
		InspectRetentionDays:  retainDays,
		InspectRetentionMax:   retainMax,
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
	ui.IconPath = ResolveIcon(etcDir, ui.IconName)
	return ui
}

func uiFromVals(fileVals map[string]string) UISettings {
	return UISettings{
		Title:      firstNonEmpty(fileVals["title"], defaultTitle),
		Tagline:    FixedTagline,
		BannerName: fileVals["banner"],
		IconName:   fileVals["icon"],
	}
}

// ResolveBanner 仅使用 etc 目录下自有图片；无则返回空（前端用默认样式）。
func ResolveBanner(etcDir, configured string) string {
	return resolveEtcFile(etcDir, configured, []string{
		"banner.jpg", "banner.jpeg", "banner.png", "banner.webp", "banner.gif",
	})
}

// ResolveIcon 使用 etc 下 jpg/png；无则返回空（路由回退到内置马里奥 8）。
func ResolveIcon(etcDir, configured string) string {
	return resolveEtcFile(etcDir, configured, []string{
		"icon.jpg", "icon.jpeg", "icon.png",
	})
}

func resolveEtcFile(etcDir, configured string, fallbacks []string) string {
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, fallbacks...)
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

func parsePositiveInt(raw string, def int) int {
	return parseBoundedInt(raw, def, 1000)
}

// parseBoundedInt parses a positive integer. Empty, zero, non-numeric, or above max returns def.
func parseBoundedInt(raw string, def, max int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" || max <= 0 {
		return def
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > max {
			return def
		}
	}
	if n <= 0 {
		return def
	}
	return n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
