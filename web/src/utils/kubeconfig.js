function unquote(s) {
  const t = String(s || '').trim()
  if ((t.startsWith('"') && t.endsWith('"')) || (t.startsWith("'") && t.endsWith("'"))) {
    return t.slice(1, -1)
  }
  return t
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

  const lines = String(text || '').replace(/\r\n/g, '\n').replace(/\r/g, '\n').split('\n')
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
  if (!String(text || '').trim()) return ''
  const { clusters, contexts, currentContext } = parseKubeconfigLite(text)

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
  return ''
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

/** serverOverride wins when set; else kubeconfig current-context cluster server. */
export function suggestHostsHostname({ kubeconfig = '', serverOverride = '' } = {}) {
  const override = String(serverOverride || '').trim()
  const server = override || suggestServerFromKubeconfig(kubeconfig)
  const hostname = hostnameFromServerURL(server)
  if (!hostname || isLiteralIP(hostname)) return ''
  return hostname
}
