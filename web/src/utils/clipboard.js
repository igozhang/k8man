/** Clipboard helper with optional copied-token callback. */
export async function copyText(text) {
  const t = String(text ?? '')
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(t)
    return
  }
  const ta = document.createElement('textarea')
  ta.value = t
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.left = '-9999px'
  document.body.appendChild(ta)
  ta.select()
  document.execCommand('copy')
  document.body.removeChild(ta)
}
