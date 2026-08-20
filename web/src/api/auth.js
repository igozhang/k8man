import http from './http'

export const authApi = {
  igokey: ({ renew = false } = {}) =>
    http.get('/auth/igokey', { params: renew ? { renew: 1 } : undefined }),
  login: (key) => http.post('/auth/login', { key }),
  session: (token) => http.post('/auth/session', { token })
}
