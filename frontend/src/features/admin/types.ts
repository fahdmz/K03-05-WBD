export type User = {
  id: number
  username: string
  isAdmin: boolean
  isActive: boolean
}

export type Access = {
  databaseId: number
  level: 'view' | 'edit'
}

export type Group = {
  id: number
  name: string
  description: string
  leaderUserId: number | null
  memberIds: number[]
  access: Access[]
}

export type Database = {
  id: number
  name: string
  host: string
  port: number
  dbname: string
  username: string
  sslmode: string
}