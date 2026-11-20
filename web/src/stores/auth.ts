import { reactive } from 'vue'
import { api } from '../api'
import type { Admin } from '../types'

export const auth = reactive<{
  admin: Admin | null
  ready: boolean
}>({
  admin: null,
  ready: false,
})

export async function loadMe() {
  try {
    const res = await api.get<{ admin: Admin }>('/auth/me')
    auth.admin = res.admin
  } catch {
    auth.admin = null
  } finally {
    auth.ready = true
  }
}

export async function login(username: string, password: string) {
  const res = await api.post<{ admin: Admin }>('/auth/login', { username, password })
  auth.admin = res.admin
  auth.ready = true
}

export async function logout() {
  try {
    await api.post('/auth/logout')
  } finally {
    auth.admin = null
  }
}
