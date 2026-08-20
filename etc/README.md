# etc 目录

| 文件 | 说明 |
|------|------|
| `k8man.conf` | 运行参数。头部为生效配置，尾部为说明与示例（含巡检行为：约 2 小时一轮，无独立配置项）。缺失或无效时启动会自动生成默认文件。其中 `title` 为页眉左侧品牌名（兼浏览器标签页标题），`tagline` 为站名下方 slogan（默认 `practice makes perfect`）。 |
| `hosts` | 私有解析，格式 `IP hostname`。与「KubeConfig」页 Hosts 共用。 |
| `icon.jpg` / `icon.png` | **可选**站点 icon。有则作为 favicon 与页眉图标；无则使用内置马里奥风格（红白蓝）数字 8。 |
| `banner.jpg` 等 | **可选**自有头部 banner（jpg/png/webp/gif）。有则使用，无则默认面板风格。 |

巡检快照不在 etc，而在 SQLite（`<data_dir>/k8man.db` 的 `cluster_inspections`）。
写操作 igokey 活跃密钥在 `<data_dir>/igokey-active.json`（不在 etc）。

不依赖外部站点图片；样式仅参考同类站点的浅青配色与圆角 banner 布局。
