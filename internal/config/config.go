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
# | hosts     | （文件）         | <etc>/hosts             | 私有解析，格式：IP hostname |
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
# 巡检（进程内，无独立配置项，约 2 小时一轮）：
#   对每个已保存 kubeconfig 独立计时（完成后 ±6 分钟抖动），串行执行
#   （同时只跑 1 个，间隔约 15 秒）。先 Probe（与页面「测试」相同并回写状态），
#   成功再跑 Diagnose（节点/负载/Pod/事件；project 跳过节点）。
#   进程退出或请求取消时不落库，避免把关机写成 kubeconfig 无效。
#   结果在「监控」页底部摘要；手动巡检在页内选择 kubeconfig 后点「一键巡检」
#   （先展示缓存快照，再实时 Probe + 诊断并更新）。
#   改 kubeconfig / API Server / TLS 会丢掉该配置的旧巡检快照。
#
# ----- 示例 -----
# db_path=./data/k8man.db
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

	return Config{
		Addr:       addr,
		DBPath:     dbPath,
		DataDir:    dataDir,
		EtcDir:     etcDir,
		Title:      ui.Title,
		BannerName: ui.BannerName,
		IconName:   ui.IconName,
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
