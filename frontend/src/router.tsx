import { createBrowserRouter } from 'react-router-dom'
import { NotFoundPage } from './components/ErrorPages'
import { RequireAdmin, RequireAuth } from './components/guards'
import Layout from './components/Layout'
import AdminPage from './features/admin/AdminPage'
import LoginPage from './features/auth/LoginPage'
import { useAuth } from './lib/auth'

function Placeholder({ title }: { title: string }) {
    return <p className="text-slate-500">{title}</p>
}

function AdminRoute() {
    const { user } = useAuth()
    return <AdminPage currentUserId={user?.id} />
}

export const router = createBrowserRouter([
    { path: '/login', element: <LoginPage /> },
    {
        element: <RequireAuth />,
        children: [
            {
                element: <Layout />,
                children: [
                    { path: '/', element: <Placeholder title="Daftar database" /> },
                    {
                        element: <RequireAdmin />,
                        children: [{ path: '/admin', element: <AdminRoute /> }],
                    },
                    { path: '*', element: <NotFoundPage /> },
                ],
            },
        ],
    },
])