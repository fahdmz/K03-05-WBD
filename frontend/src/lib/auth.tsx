import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { api, setCsrfToken, setUnauthorizedHandler } from './api'
import type { SessionResponse, User } from './types'

type AuthState = {
    user: User | null
    loading: boolean
    login: (username: string, password: string) => Promise<void>
    logout: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
    const [user, setUser] = useState<User | null>(null)
    const [loading, setLoading] = useState(true)

    useEffect(() => {
        setUnauthorizedHandler(() => setUser(null))
        api<SessionResponse>('/auth/me').then((data) => {
            setCsrfToken(data.csrfToken)
            setUser(data.user)
        }).catch(() => setUser(null)).finally(() => setLoading(false))
    }, [])

    async function login(username: string, password: string) {
        const data = await api<SessionResponse>('/auth/login', {
            method: 'POST',
            body: { username, password }
        })
        setCsrfToken(data.csrfToken)
        setUser(data.user)
    }

    async function logout() {
        try {
            await api('/auth/logout', { method: 'POST' })
        } finally {
            setCsrfToken('')
            setUser(null)
        }
    }

    return (
        <AuthContext.Provider value={{ user, loading, login, logout }}>
            {children}
        </AuthContext.Provider>
    )


}
export function useAuth() {
    const ctx = useContext(AuthContext)
    if (!ctx) throw new Error('useAuth harus dipakai di dalam AuthProvider')
    return ctx
}