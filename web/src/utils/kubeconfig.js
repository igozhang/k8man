function unquote(s) {
  const t = String(s || '').trim()
  if ((t.startsWith('"') && t.endsWith('"')) || (t.startsWith("'") && t.endsWith("'"))) {
    return t.slice(1, -1)
  }
  return t
}

function normalizeKubeconfigText(text) {
  return String(text || '').replace(/^\uFEFF/, '').replace(/\r\n/g, '\n').replace(/\r/g, '\n')
}

/** Lightweight kubeconfig parse: clusters/contexts/current-context only. */
export function parseKubeconfigLite(text) {
  const clusters = new Map()
  const contexts = new Map()
  let currentContext = ''

  let clusterServer = ''
  let clusterName = ''
  let contextCluster = ''
  let contextName = ''
  let section = ''

  function flushCluster() {
    if (clusterName && clusterServer) clusters.set(clusterName, clusterServer)
    clusterServer = ''
    clusterName = ''
  }

  function flushContext() {
    if (contextName && contextCluster) contexts.set(contextName, contextCluster)
    contextCluster = ''
    contextName = ''
  }

  const lines = normalizeKubeconfigText(text).split('\n')
  for (const rawLine of lines) {
    const t = rawLine.trim()
    if (!t || t.startsWith('#')) continue

    const indent = rawLine.length - rawLine.trimStart().length
    if (indent === 0) {
      if (section === 'clusters') flushCluster()
      if (section === 'contexts') flushContext()
      if (t.startsWith('current-context:')) {
        currentContext = unquote(t.slice('current-context:'.length).trim())
      } else if (t === 'clusters:') {
        section = 'clusters'
      } else if (t === 'contexts:') {
        section = 'contexts'
      } else if (t === 'users:') {
        if (section === 'clusters') flushCluster()
        if (section === 'contexts') flushContext()
        section = ''
      } else if (
        t.startsWith('apiVersion:')
        || t.startsWith('kind:')
        || t.startsWith('preferences:')
      ) {
        if (section === 'clusters') flushCluster()
        if (section === 'contexts') flushContext()
        section = ''
      }
      continue
    }

    if (section === 'clusters') {
      if (t.startsWith('- ')) {
        flushCluster()
        if (t.includes('name:')) {
          clusterName = unquote(t.replace(/^-\s*/, '').replace(/^name:\s*/, ''))
        }
        continue
      }
      if (t.startsWith('name:')) clusterName = unquote(t.slice('name:'.length).trim())
      else if (t.startsWith('server:')) clusterServer = unquote(t.slice('server:'.length).trim())
      continue
    }

    if (section === 'contexts') {
      if (t.startsWith('- ')) {
        flushContext()
        continue
      }
      if (t.startsWith('name:')) contextName = unquote(t.slice('name:'.length).trim())
      else if (t.startsWith('cluster:')) contextCluster = unquote(t.slice('cluster:'.length).trim())
    }
  }
  if (section === 'clusters') flushCluster()
  if (section === 'contexts') flushContext()

  return { clusters, contexts, currentContext }
}

/** Mirrors backend SuggestServerFromKubeconfig. */
export function suggestServerFromKubeconfig(text) {
  const raw = normalizeKubeconfigText(text)
  if (!raw.trim()) return ''
  const { clusters, contexts, currentContext } = parseKubeconfigLite(raw)

  let ctxName = currentContext
  if (!ctxName) {
    for (const name of contexts.keys()) {
      ctxName = name
      break
    }
  }
  if (ctxName && contexts.has(ctxName)) {
    const clusterName = contexts.get(ctxName)
    if (clusterName && clusters.has(clusterName)) return clusters.get(clusterName)
  }
  for (const server of clusters.values()) {
    if (server) return server
  }
  const m = raw.match(/^\s*server:\s*(\S+)/m)
  return m?.[1] ? unquote(m[1]) : ''
}

export function hostnameFromServerURL(server) {
  const raw = String(server || '').trim()
  if (!raw) return ''
  try {
    return new URL(raw).hostname.toLowerCase()
  } catch {
    try {
      return new URL(`https://${raw}`).hostname.toLowerCase()
    } catch {
      return ''
    }
  }
}

export function isLiteralIP(host) {
  const h = String(host || '').trim()
  if (!h) return false
  if (/^\d+\.\d+\.\d+\.\d+$/.test(h)) return true
  if (h.includes(':')) return true
  return false
}

export function normalizeHostname(h) {
  return String(h || '').trim().toLowerCase().replace(/\.+$/, '')
}

/** Domain hostnames from API Server override, then kubeconfig cluster servers. */
export function collectKubeconfigHostnames({ kubeconfig = '', serverOverride = '' } = {}) {
  const out = []
  const seen = new Set()
  const addServer = (server) => {
    const host = normalizeHostname(hostnameFromServerURL(server))
    if (!host || isLiteralIP(host) || seen.has(host)) return
    seen.add(host)
    out.push(host)
  }
  addServer(serverOverride)
  addServer(suggestServerFromKubeconfig(kubeconfig))
  const { clusters } = parseKubeconfigLite(kubeconfig)
  for (const server of clusters.values()) addServer(server)
  return out
}

/** serverOverride domain wins; if override is an IP, fall back to kubeconfig. */
export function suggestHostsHostname({ kubeconfig = '', serverOverride = '' } = {}) {
  return collectKubeconfigHostnames({ kubeconfig, serverOverride })[0] || ''
}

/** Look up an existing hosts-file IP for hostname (case-insensitive). */
export function lookupHostsIP(hostname, hosts = []) {
  const host = normalizeHostname(hostname)
  if (!host) return ''
  const list = Array.isArray(hosts) ? hosts : []
  for (const row of list) {
    const name = normalizeHostname(row?.hostname || row?.id)
    if (name === host) return String(row?.ip || '').trim()
  }
  return ''
}

/**
 * Prefill hosts line: reuse existing IP when any detected hostname is already in Hosts;
 * otherwise fall back to defaultIP (placeholder for the user to replace).
 */
export function suggestHostsLine({
  kubeconfig = '',
  serverOverride = '',
  hosts = [],
  defaultIP = '10.10.10.10'
} = {}) {
  const hostnames = collectKubeconfigHostnames({ kubeconfig, serverOverride })
  if (!hostnames.length) return { hostname: '', ip: '', line: '', fromHosts: false }

  for (const hostname of hostnames) {
    const existing = lookupHostsIP(hostname, hosts)
    if (existing) {
      return {
        hostname,
        ip: existing,
        line: `${existing} ${hostname}`,
        fromHosts: true
      }
    }
  }

  const hostname = hostnames[0]
  return {
    hostname,
    ip: defaultIP,
    line: `${defaultIP} ${hostname}`,
    fromHosts: false
  }
}
