package admin

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

type SessionManager interface {
	RevokeUserSessions(ctx context.Context, userID int64) error
	RotateUserSessions(ctx context.Context, userID int64) error
}

type SessionManagerFactory func(tx pgx.Tx) SessionManager

type sessionChanges struct {
	revoke map[int64]bool
	rotate map[int64]bool
}

func newSessionChanges() *sessionChanges {
	return &sessionChanges{
		revoke: make(map[int64]bool),
		rotate: make(map[int64]bool),
	}
}

func (c *sessionChanges) Revoke(userID int64) {
	c.revoke[userID] = true
	delete(c.rotate, userID)
}

func (c *sessionChanges) Rotate(userID int64) {
	if !c.revoke[userID] {
		c.rotate[userID] = true
	}
}

func (c *sessionChanges) Apply(
	ctx context.Context,
	manager SessionManager,
) error {
	ids := make([]int64, 0, len(c.revoke)+len(c.rotate))

	for id := range c.revoke {
		ids = append(ids, id)
	}
	for id := range c.rotate {
		ids = append(ids, id)
	}

	// Use a consistent lock order when several users are affected.
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})

	for _, id := range ids {
		var err error

		if c.revoke[id] {
			err = manager.RevokeUserSessions(ctx, id)
		} else {
			err = manager.RotateUserSessions(ctx, id)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func membershipSessionChanges(
	before, after []int64,
) *sessionChanges {
	changes := newSessionChanges()
	oldMembers := make(map[int64]bool, len(before))
	newMembers := make(map[int64]bool, len(after))

	for _, id := range before {
		oldMembers[id] = true
	}
	for _, id := range after {
		newMembers[id] = true
	}

	for id := range oldMembers {
		if !newMembers[id] {
			changes.Revoke(id)
		}
	}
	for id := range newMembers {
		if !oldMembers[id] {
			changes.Rotate(id)
		}
	}

	return changes
}

func accessSessionChanges(
	memberIDs []int64,
	before, after []groupAccessEntry,
) *sessionChanges {
	changes := newSessionChanges()

	level := func(value string) int {
		switch value {
		case "view":
			return 1
		case "edit":
			return 2
		default:
			return 0
		}
	}

	oldLevels := make(map[int64]int)
	newLevels := make(map[int64]int)

	for _, entry := range before {
		oldLevels[entry.DatabaseID] = level(entry.Level)
	}
	for _, entry := range after {
		newLevels[entry.DatabaseID] = level(entry.Level)
	}

	decreased := false
	increased := false

	for id, oldLevel := range oldLevels {
		if newLevels[id] < oldLevel {
			decreased = true
		}
	}
	for id, newLevel := range newLevels {
		if newLevel > oldLevels[id] {
			increased = true
		}
	}

	for _, userID := range memberIDs {
		if decreased {
			changes.Revoke(userID)
		} else if increased {
			changes.Rotate(userID)
		}
	}

	return changes
}