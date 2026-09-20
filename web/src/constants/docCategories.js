/** 运维文档分类：value 存库，label 展示 */
export const DOC_CATEGORIES = [
  { value: 'general', label: 'general' },
  { value: 'k8s', label: 'k8s' },
  { value: 'devops', label: 'devops' },
  { value: 'monitoring', label: 'monitoring' },
  { value: 'inspection-report', label: '巡检报告' }
]

export const DOC_CATEGORY_INSPECTION_REPORT = 'inspection-report'

export function docCategoryLabel(value) {
  const v = String(value || '').trim()
  const hit = DOC_CATEGORIES.find((c) => c.value === v)
  return hit?.label || v || '—'
}

/** 日·所有基地巡检报告的地址是 /docs/inspectallYYYYMMDD */
export function docPublicId(doc) {
  const slug = String(doc?.slug || '').trim()
  if (/^inspectall\d{8}$/.test(slug)) return slug
  const id = String(doc?.id || '').trim()
  return id
}
