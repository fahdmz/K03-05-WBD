import { Navigate, Outlet } from 'react-router-dom'
import { useAuth } from '../lib/auth'
import { ForbiddenPage } from './ErrorPages'

export function RequireAuth() {
    const { user, loading } = useAuth()
    if (loading) return <p className="p-6 text-slate-500">Memuat...</p>
    if (!user) return <Navigate to="/login" replace />
    return <Outlet />
}

export function RequireAdmin() {
    const { user } = useAuth()
    if (!user?.isAdmin) return <ForbiddenPage />
    return <Outlet />
}