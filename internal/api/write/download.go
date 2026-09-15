package write

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"

	"gitee.com/k8man/internal/kube"
)

const (
	downloadProbeTimeout  = 20 * time.Second
	downloadStreamTimeout = 10 * time.Minute
)

var beijingLoc = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()

var (
	dlSeqMu sync.Mutex
	dlSeq   = map[string]int{} // key: scope\x00basename\x00YYYYMMDDHH → last seq
)

type pathKind int

const (
	pathMissing pathKind = iota
	pathFile
	pathDir
	pathOther
)

// delayedAttachment writes Content-Disposition only after the first payload byte
// (or finishEmpty), so probe/exec failures can still return JSON.
type delayedAttachment struct {
	c           *gin.Context
	ctype       string
	resolveName func() string
	filename    string
	started     bool
}

func (d *delayedAttachment) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	d.start()
	return d.c.Writer.Write(p)
}

func (d *delayedAttachment) start() {
	if d.started {
		return
	}
	d.started = true
	if d.filename == "" && d.resolveName != nil {
		d.filename = d.resolveName()
	}
	if d.filename == "" {
		d.filename = "download"
	}
	setAttachmentHeaders(d.c, d.filename, d.ctype)
}

func (d *delayedAttachment) finishEmpty() {
	d.start()
}

// PodDownload streams a file or tar.gz directory from a container.
// Query: container (optional), path (required, absolute).
func (h *Handler) PodDownload(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	clusterID := c.Param("id")
	ns, name := c.Param("ns"), c.Param("name")
	container := strings.TrimSpace(c.Query("container"))
	rawPath := strings.TrimSpace(c.Query("path"))

	clean, err := sanitizePodPath(rawPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}

	probeCtx, probeCancel := context.WithTimeout(c.Request.Context(), downloadProbeTimeout)
	pod, err := clients.GetPod(probeCtx, ns, name)
	if err != nil {
		probeCancel()
		c.JSON(http.StatusBadGateway, gin.H{"code": 502, "message": err.Error()})
		return
	}
	names := kube.PodContainerNames(pod)
	if len(names) == 0 {
		probeCancel()
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "pod has no containers"})
		return
	}
	phase := string(pod.Status.Phase)
	if phase != "" && phase != string(corev1.PodRunning) {
		probeCancel()
		c.JSON(http.StatusConflict, gin.H{
			"code":    409,
			"message": "pod is not Running (phase=" + phase + ")",
		})
		return
	}
	if container == "" {
		container = preferReadyContainer(pod, names)
	} else {
		found := false
		for _, n := range names {
			if n == container {
				found = true
				break
			}
		}
		if !found {
			probeCancel()
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "container not found on pod"})
			return
		}
	}

	kind, err := probePodPath(probeCtx, clients, ns, name, container, clean)
	probeCancel()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"code": 502, "message": kube.FriendlyConnectivityError(err)})
		return
	}
	switch kind {
	case pathMissing:
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "路径不存在: " + clean})
		return
	case pathOther:
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "仅支持普通文件或目录: " + clean})
		return
	}

	streamCtx, streamCancel := context.WithTimeout(c.Request.Context(), downloadStreamTimeout)
	defer streamCancel()

	switch kind {
	case pathFile:
		filename := safeDownloadName(path.Base(clean))
		if filename == "" {
			filename = "download"
		}
		out := &delayedAttachment{c: c, filename: filename, ctype: "application/octet-stream"}
		var stderr bytes.Buffer
		err := clients.Exec(streamCtx, kube.ExecOptions{
			Namespace: ns,
			Pod:       name,
			Container: container,
			// redirect avoids `cat --` (BusyBox) and `cat -name` flag parsing
			Command: []string{"sh", "-c", `cat < "$1"`, "dl", clean},
			Stdout:  out,
			Stderr:  &stderr,
		})
		finishDownload(c, out, err, &stderr)
	case pathDir:
		parent, base, splitErr := splitDirForTar(clean)
		if splitErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": splitErr.Error()})
			return
		}
		tarCtx, tarCancel := context.WithTimeout(c.Request.Context(), downloadProbeTimeout)
		tarErr := ensurePodTar(tarCtx, clients, ns, name, container)
		tarCancel()
		if tarErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": tarErr.Error()})
			return
		}
		scope := strings.Join([]string{clusterID, ns, name, container, clean}, "/")
		out := &delayedAttachment{
			c:     c,
			ctype: "application/gzip",
			resolveName: func() string {
				return nextDirArchiveName(scope, base, time.Now().In(beijingLoc))
			},
		}
		var stderr bytes.Buffer
		// -C before -f: BusyBox tar treats a late -C as a filename
		err := clients.Exec(streamCtx, kube.ExecOptions{
			Namespace: ns,
			Pod:       name,
			Container: container,
			Command:   []string{"tar", "-C", parent, "-czf", "-", base},
			Stdout:    out,
			Stderr:    &stderr,
		})
		finishDownload(c, out, err, &stderr)
	}
}

func finishDownload(c *gin.Context, out *delayedAttachment, err error, stderr *bytes.Buffer) {
	if out.started {
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"code": 502, "message": downloadExecMessage(err, stderr)})
		return
	}
	out.finishEmpty()
}

func downloadExecMessage(err error, stderr *bytes.Buffer) string {
	msg := strings.TrimSpace(stderr.String())
	if msg != "" {
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		return msg
	}
	if err != nil {
		return kube.FriendlyConnectivityError(err)
	}
	return "下载失败"
}

func setAttachmentHeaders(c *gin.Context, filename, contentType string) {
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", contentDisposition(filename))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
}

func contentDisposition(filename string) string {
	ascii := safeASCIIFilename(filename)
	escaped := url.PathEscape(filename)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, escaped)
}

func safeASCIIFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." {
		return "download"
	}
	return s
}

func sanitizePodPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	for _, r := range raw {
		if r == 0 || unicode.IsControl(r) {
			return "", fmt.Errorf("path 非法")
		}
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("path 必须是绝对路径")
	}
	clean := path.Clean(raw)
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path 不允许包含 ..")
		}
	}
	if clean == "" {
		return "", fmt.Errorf("path 非法")
	}
	return clean, nil
}

// splitDirForTar returns parent and basename for `tar -C parent base`.
func splitDirForTar(clean string) (parent, base string, err error) {
	clean = path.Clean(clean)
	if clean == "/" {
		return "", "", fmt.Errorf("不支持打包根目录 /")
	}
	base = path.Base(clean)
	parent = path.Dir(clean)
	if base == "" || base == "." || base == "/" {
		return "", "", fmt.Errorf("无法解析目录名")
	}
	if parent == "" {
		parent = "/"
	}
	return parent, base, nil
}

func probePodPath(ctx context.Context, clients *kube.Clients, ns, pod, container, clean string) (pathKind, error) {
	var stdout, stderr bytes.Buffer
	script := `p="$1"; if [ -d "$p" ]; then echo DIR; elif [ -f "$p" ]; then echo FILE; elif [ -e "$p" ]; then echo OTHER; else echo MISSING; fi`
	err := clients.Exec(ctx, kube.ExecOptions{
		Namespace: ns,
		Pod:       pod,
		Container: container,
		Command:   []string{"sh", "-c", script, "probe", clean},
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	out := strings.TrimSpace(stdout.String())
	if err != nil && out == "" {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return pathMissing, fmt.Errorf("%s", msg)
		}
		return pathMissing, err
	}
	switch out {
	case "DIR":
		return pathDir, nil
	case "FILE":
		return pathFile, nil
	case "OTHER":
		return pathOther, nil
	default:
		return pathMissing, nil
	}
}

func ensurePodTar(ctx context.Context, clients *kube.Clients, ns, pod, container string) error {
	var stdout, stderr bytes.Buffer
	err := clients.Exec(ctx, kube.ExecOptions{
		Namespace: ns,
		Pod:       pod,
		Container: container,
		Command:   []string{"sh", "-c", "command -v tar >/dev/null 2>&1 && echo OK || echo NO"},
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("检查 tar 失败: %s", msg)
		}
		return fmt.Errorf("检查 tar 失败: %w", err)
	}
	if strings.TrimSpace(stdout.String()) != "OK" {
		return fmt.Errorf("容器内无 tar，无法打包目录")
	}
	return nil
}

func nextDirArchiveName(scope, dirBase string, now time.Time) string {
	name := safeDownloadName(dirBase)
	if name == "" {
		name = "dir"
	}
	stamp := now.Format("2006010215")
	key := scope + "\x00" + name + "\x00" + stamp
	dlSeqMu.Lock()
	defer dlSeqMu.Unlock()
	pruneDlSeqLocked(stamp)
	n := dlSeq[key] + 1
	dlSeq[key] = n
	return fmt.Sprintf("%s%s%02d.tar.gz", name, stamp, n)
}

func pruneDlSeqLocked(currentStamp string) {
	if len(dlSeq) < 256 {
		return
	}
	for k := range dlSeq {
		i := strings.LastIndexByte(k, 0)
		if i < 0 || i+1 >= len(k) {
			delete(dlSeq, k)
			continue
		}
		if k[i+1:] < currentStamp {
			delete(dlSeq, k)
		}
	}
}

func safeDownloadName(base string) string {
	base = strings.TrimSpace(base)
	base = strings.ReplaceAll(base, "/", "")
	base = strings.ReplaceAll(base, "\\", "")
	base = strings.ReplaceAll(base, "\x00", "")
	if base == "" || base == "." || base == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range base {
		if unicode.IsControl(r) {
			continue
		}
		switch r {
		case '"', '\'', '<', '>', '|', ':', '*', '?', '\\':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
