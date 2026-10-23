export type User = {
    id: number
    username: string
    isAdmin: boolean
    isActive: boolean
}

export type SessionResponse = {
    user: User
    csrfToken: string
}