package kube

import "strings"

// EvidenceValue returns the value for key= from issue evidence (case-insensitive key).
func EvidenceValue(ev []string, key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return ""
	}
	prefix := key + "="
	for _, line := range ev {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if strings.HasPrefix(low, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

var nfsServerEvidenceKeys = []string{
	"server", "nfsServer", "nfs.server", "nfs-server", "nfs_server",
	"addr", "address", "host", "hostname", "serverAddr", "serverAddress",
}

var nfsPathEvidenceKeys = []string{
	"path", "share", "nfsPath", "nfs.path", "nfs-path", "nfs_path",
	"export", "baseDir", "basePath",
}

// IssueNFSServer resolves NFS server IP/hostname from NodeIP or evidence.
func IssueNFSServer(it Issue) string {
	if s := sanitizeNFSServer(it.NodeIP); s != "" {
		return s
	}
	for _, k := range nfsServerEvidenceKeys {
		if v := sanitizeNFSServer(EvidenceValue(it.Evidence, k)); v != "" {
			return v
		}
	}
	return ""
}

// IssueNFSPath resolves NFS export path from evidence.
func IssueNFSPath(it Issue) string {
	for _, k := range nfsPathEvidenceKeys {
		if v := sanitizeNFSPath(EvidenceValue(it.Evidence, k)); v != "" {
			return v
		}
	}
	return ""
}

// IssueLocationLines returns human-readable location hints for alerts/UI.
func IssueLocationLines(it Issue) []string {
	reason := strings.TrimSpace(it.Reason)
	switch {
	case reason == "NFSHighUsage" || it.Kind == "StorageClass":
		if s := IssueNFSServer(it); s != "" {
			if p := IssueNFSPath(it); p != "" {
				return []string{"NFS " + FormatNFSEndpoint(s, p)}
			}
			return []string{"NFS服务器 " + s}
		}
	case it.Category == "node" || it.Kind == "Node":
		if ip := strings.TrimSpace(it.NodeIP); ip != "" {
			if name := strings.TrimSpace(it.Name); name != "" && name != ip {
				return []string{name + " (" + ip + ")"}
			}
			return []string{"IP " + ip}
		}
	case it.Category == "pod" || it.Kind == "Pod":
		var parts []string
		if n := strings.TrimSpace(it.Node); n != "" {
			parts = append(parts, n)
		}
		if ip := strings.TrimSpace(it.NodeIP); ip != "" {
			parts = append(parts, "("+ip+")")
		}
		if len(parts) > 0 {
			return []string{"节点 " + strings.Join(parts, " ")}
		}
	}
	return nil
}

func messageContainsLoc(msg, token string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg))
	token = strings.ToLower(strings.TrimSpace(token))
	if msg == "" || token == "" {
		return false
	}
	// Avoid tiny tokens matching noise (e.g. single-digit scraps).
	if len([]rune(token)) < 2 {
		return false
	}
	return strings.Contains(msg, token)
}

// EnrichIssueMessage injects missing location (NFS server, node IP, pod node) into the message text.
func EnrichIssueMessage(it Issue) string {
	msg := strings.TrimSpace(it.Message)
	if msg == "" {
		return msg
	}
	switch strings.TrimSpace(it.Reason) {
	case "NFSHighUsage":
		return enrichNFSIssueMessage(msg, it)
	}
	if it.Category == "node" || it.Kind == "Node" {
		return enrichNodeIssueMessage(msg, it)
	}
	// Pod issues (incl. LargeEphemeral under category=storage)
	if it.Category == "pod" || it.Kind == "Pod" {
		return enrichPodIssueMessage(msg, it)
	}
	return msg
}

// FinalizeIssueMessages writes location-enriched text back into each issue Message
// so alerts, API payloads, and UI all share the same locating details.
func FinalizeIssueMessages(issues []Issue) {
	for i := range issues {
		if m := EnrichIssueMessage(issues[i]); m != "" {
			issues[i].Message = clipRunes(m, diagnoseMsgMaxRunes)
		}
	}
}

func enrichNFSIssueMessage(msg string, it Issue) string {
	server := IssueNFSServer(it)
	if server == "" {
		return msg
	}
	ep := FormatNFSEndpoint(server, IssueNFSPath(it))
	if messageContainsLoc(msg, server) || (ep != "" && messageContainsLoc(msg, ep)) {
		return msg
	}
	rest := strings.TrimPrefix(msg, "NFS")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "NFS " + ep
	}
	// Legacy: "NFS 使用率 …" or "NFS（SC=…）使用率 …" / "NFS SC=… 使用率 …" without server
	if strings.HasPrefix(rest, "使用率") || strings.HasPrefix(rest, "（SC=") || strings.HasPrefix(rest, "(SC=") {
		return "NFS " + ep + " " + rest
	}
	if strings.HasPrefix(rest, "SC=") {
		if i := strings.IndexByte(rest, ' '); i > 0 {
			return "NFS " + ep + rest[i:]
		}
		return "NFS " + ep
	}
	return "NFS " + ep + " · " + msg
}

func enrichNodeIssueMessage(msg string, it Issue) string {
	ip := strings.TrimSpace(it.NodeIP)
	if ip == "" || messageContainsLoc(msg, ip) {
		return msg
	}
	name := strings.TrimSpace(it.Name)
	if name != "" && !messageContainsLoc(msg, name) {
		return msg + " · " + name + " (" + ip + ")"
	}
	return msg + " · IP " + ip
}

func enrichPodIssueMessage(msg string, it Issue) string {
	ip := strings.TrimSpace(it.NodeIP)
	node := strings.TrimSpace(it.Node)
	if ip == "" && node == "" {
		return msg
	}
	if ip != "" && messageContainsLoc(msg, ip) {
		return msg
	}
	if node != "" && messageContainsLoc(msg, node) {
		return msg
	}
	var parts []string
	if node != "" {
		parts = append(parts, node)
	}
	if ip != "" {
		parts = append(parts, "("+ip+")")
	}
	return msg + " · 节点 " + strings.Join(parts, " ")
}
