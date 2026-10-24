import { createBrowserRouter } from 'react-router-dom'
import { NotFoundPage } from './components/ErrorPages'
import { RequireAdmin, RequireAuth } from './components/guards'
import Layout from './components/Layout'
import LoginPage from './features/auth/LoginPage'

function Placeholder({ title }: { title: string }) {
    return <p className="text-slate-500">{title}</p>
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
                        children: [{ path: '/admin', element: <Placeholder title="Admin" /> }],
                    },
                    { path: '*', element: <NotFoundPage /> },
                ],
            },
        ],
    },
])