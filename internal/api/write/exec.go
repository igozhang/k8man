package write

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/remotecommand"

	"gitee.com/k8man/internal/kube"
)

const (
	execIdleTimeout    = 30 * time.Minute
	execIdleCheckEvery = 30 * time.Second
	execWSReadIdle     = 60 * time.Second
	execPingEvery      = 20 * time.Second
)

var (
	errExecIdleTimeout = errors.New("空闲超时：超过 30 分钟无输入，已断开")
)

var execUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type execCtrlMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

type terminalSizeQueue struct {
	mu     sync.Mutex
	ch     chan remotecommand.TerminalSize
	closed bool
}

func newTerminalSizeQueue() *terminalSizeQueue {
	return &terminalSizeQueue{ch: make(chan remotecommand.TerminalSize, 8)}
}

func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

func (q *terminalSizeQueue) Push(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	select {
	case q.ch <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
		select {
		case <-q.ch:
		default:
		}
		select {
		case q.ch <- remotecommand.TerminalSize{Width: cols, Height: rows}:
		default:
		}
	}
}

func (q *terminalSizeQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.ch)
}

type wsConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *wsConn) writeBinary(p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return w.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (w *wsConn) writeJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return w.conn.WriteJSON(v)
}

type wsStdout struct {
	w *wsConn
}

func (s wsStdout) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := s.w.writeBinary(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// PodExec upgrades to WebSocket and bridges an interactive shell into the pod.
// Query: container (optional), cols/rows (optional initial size).
// Frames: client binary → stdin; client text JSON resize; server binary → stdout.
// Session closes after 30m without user stdin (resize / WS ping do not count).
func (h *Handler) PodExec(c *gin.Context) {
	clients, ok := h.clients(c)
	if !ok {
		return
	}
	ns, name := c.Param("ns"), c.Param("name")
	container := strings.TrimSpace(c.Query("container"))

	probeCtx, probeCancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer probeCancel()
	pod, err := clients.GetPod(probeCtx, ns, name)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"code": 502, "message": err.Error()})
		return
	}
	names := kube.PodContainerNames(pod)
	if len(names) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "pod has no containers"})
		return
	}
	phase := string(pod.Status.Phase)
	if phase != "" && phase != string(corev1.PodRunning) {
		c.JSON(http.StatusConflict, gin.H{
			"code":    409,
			"message": "pod is not Running (phase=" + phase + ")",
		})
		return
	}
	if container == "" {
		container = names[0]
	} else {
		found := false
		for _, n := range names {
			if n == container {
				found = true
				break
			}
		}
		if !found {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "container not found on pod"})
			return
		}
	}

	conn, err := execUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(execWSReadIdle))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(execWSReadIdle))
		return nil
	})

	ws := &wsConn{conn: conn}
	_ = ws.writeJSON(execCtrlMsg{Type: "status", Data: "connected"})

	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()
	defer stdinWriter.Close()

	sizes := newTerminalSizeQueue()
	defer sizes.Close()

	if cols := parseUint16(c.Query("cols")); cols > 0 {
		if rows := parseUint16(c.Query("rows")); rows > 0 {
			sizes.Push(cols, rows)
		}
	}

	execCtx, execCancel := context.WithCancel(c.Request.Context())
	defer execCancel()
	go func() {
		<-execCtx.Done()
		_ = stdinWriter.Close()
		sizes.Close()
	}()

	var lastInputUnix atomic.Int64
	touchInput := func() {
		lastInputUnix.Store(time.Now().UnixNano())
	}
	touchInput()

	var idleTimedOut atomic.Bool

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stdinWriter.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				execCancel()
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(execWSReadIdle))
			switch mt {
			case websocket.BinaryMessage:
				if len(data) == 0 {
					continue
				}
				touchInput()
				if _, err := stdinWriter.Write(data); err != nil {
					execCancel()
					return
				}
			case websocket.TextMessage:
				var msg execCtrlMsg
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				switch strings.ToLower(msg.Type) {
				case "resize":
					// resize is not user shell activity
					sizes.Push(msg.Cols, msg.Rows)
				case "stdin":
					if msg.Data != "" {
						touchInput()
						if _, err := stdinWriter.Write([]byte(msg.Data)); err != nil {
							execCancel()
							return
						}
					}
				}
			}
		}
	}()

	auxStop := make(chan struct{})
	go func() {
		ping := time.NewTicker(execPingEvery)
		idle := time.NewTicker(execIdleCheckEvery)
		defer ping.Stop()
		defer idle.Stop()
		for {
			select {
			case <-auxStop:
				return
			case <-ping.C:
				ws.mu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				ws.mu.Unlock()
				if err != nil {
					execCancel()
					return
				}
			case <-idle.C:
				last := lastInputUnix.Load()
				if time.Since(time.Unix(0, last)) < execIdleTimeout {
					continue
				}
				idleTimedOut.Store(true)
				_ = ws.writeJSON(execCtrlMsg{Type: "error", Data: errExecIdleTimeout.Error()})
				execCancel()
				return
			}
		}
	}()
	defer close(auxStop)

	err = clients.ExecTTY(execCtx, kube.ExecOptions{
		Namespace: ns,
		Pod:       name,
		Container: container,
		Command:   []string{"/bin/sh"},
		Stdin:     stdinReader,
		Stdout:    wsStdout{w: ws},
		Resize:    sizes,
	})
	if !idleTimedOut.Load() && err != nil && execCtx.Err() == nil {
		_ = ws.writeJSON(execCtrlMsg{Type: "error", Data: err.Error()})
	}
	ws.mu.Lock()
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(2*time.Second))
	_ = conn.Close()
	ws.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

func parseUint16(s string) uint16 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
		if n > 65535 {
			return 0
		}
	}
	return uint16(n)
}
