/** 指标 API 失败时的友好提示（依赖 http 透传的 err.code） */
export function metricsErrorTip(err) {
  const code = err?.code
  if (code === 403) return '当前凭证无权读取指标'
  if (code === 404) return '未安装 metrics-server 或指标 API 不可用'
  return err?.message || '指标暂时不可用'
}
