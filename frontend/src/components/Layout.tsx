import { Link, Outlet } from 'react-router-dom'
import { useAuth } from '../lib/auth'

export default function Layout() {
    const { user, logout } = useAuth()

    return (
        <div className="min-h-screen bg-slate-50 text-slate-900">
            <header className="border-b border-slate-200 bg-white">
                <nav className="mx-auto flex max-w-6xl items-center gap-6 px-4 py-3">
                    <Link to="/" className="font-semibold">
                        DB Platform
                    </Link>
                    <Link to="/" className="text-sm text-slate-600 hover:text-slate-900">
                        Databases
                    </Link>
                    {user?.isAdmin && (
                        <Link to="/admin" className="text-sm text-slate-600 hover:text-slate-900">
                            Admin
                        </Link>
                    )}
                    <div className="ml-auto flex items-center gap-3 text-sm">
                        <span className="text-slate-600">{user?.username}</span>
                        <button
                            onClick={logout}
                            className="rounded border border-slate-300 px-3 py-1 hover:bg-slate-100"
                        >
                            Logout
                        </button>
                    </div>
                </nav>
            </header>
            <main className="mx-auto max-w-6xl px-4 py-6">
                <Outlet />
            </main>
        </div>
    )
}