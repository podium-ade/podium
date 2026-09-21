/**
 * The server stamps a matching CSP nonce on index.html. CodeMirror's style-mod inserts a
 * <style> tag; the policy's style-src 'self' allows that tag only when it carries this nonce.
 */
export function documentStyleNonce(): string {
  return document.querySelector('meta[name="podium-csp-nonce"]')?.getAttribute("content") ?? ""
}
