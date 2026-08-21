import { computed, ref } from 'vue'
import { readApi } from '../api'
import { TTL, cacheFresh, cacheGet, cacheKey, cacheLoad } from '../utils/resourceCache'

const DEFAULT_SITE_TITLE = 'k8s多集群运维'

const UI_KEY = cacheKey('ui', 'local')

const cachedUI = cacheGet(UI_KEY)?.data

const ui = ref({
  title: cachedUI?.title || DEFAULT_SITE_TITLE,
  hasBanner: !!cachedUI?.hasBanner,
  bannerUrl: cachedUI?.bannerUrl || '/banner',
  iconUrl: cachedUI?.iconUrl || '/icon',
  version: cachedUI?.version || '0.1.0'
})

const mediaStamp = ref(cacheGet(UI_KEY)?.at || Date.now())
let started = false

function setFavicon(href) {
  let link = document.querySelector('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  link.removeAttribute('type')
  link.href = href
}

function applyUI(data, { refreshMedia = false } = {}) {
  if (!data) return
  ui.value = {
    title: data.title || ui.value.title,
    hasBanner: !!data.hasBanner,
    bannerUrl: data.bannerUrl || '/banner',
    iconUrl: data.iconUrl || '/icon',
    version: data.version || '0.1.0'
  }
  document.title = ui.value.title
  if (refreshMedia) {
    mediaStamp.value = Date.now()
    setFavicon(`${ui.value.iconUrl}?t=${mediaStamp.value}`)
  }
}

function ensureLoaded() {
  if (started) return
  started = true
  document.title = ui.value.title
  const iconHref = `${ui.value.iconUrl || '/icon'}?t=${mediaStamp.value}`
  setFavicon(iconHref)
  const uiFresh = cacheFresh(UI_KEY, TTL.ui)
  cacheLoad(UI_KEY, TTL.ui, () => readApi.getUI())
    .then((data) => applyUI(data, { refreshMedia: !uiFresh }))
    .catch(() => {})
}

export function useSiteUI() {
  ensureLoaded()
  const bannerStyle = computed(() => {
    if (!ui.value.hasBanner) return {}
    const url = `${ui.value.bannerUrl}?t=${mediaStamp.value}`
    return { '--site-head-banner': `url('${url}')` }
  })
  const iconSrc = computed(() => `${ui.value.iconUrl || '/icon'}?t=${mediaStamp.value}`)
  return { ui, iconSrc, bannerStyle }
}
