package speaker

import "strings"

// Role decides which intents a speaker may trigger.
type Role string

// Roles, ordered any < kid < member < owner.
const (
	RoleAny    Role = "any"
	RoleKid    Role = "kid"
	RoleMember Role = "member"
	RoleOwner  Role = "owner"
)

var roleRank = map[Role]int{
	RoleAny:    0,
	RoleKid:    1,
	RoleMember: 2,
	RoleOwner:  3,
}

// ParseRole normalises a role string, defaulting to member for unknown values.
func ParseRole(s string) Role {
	role := Role(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := roleRank[role]; ok {
		return role
	}
	return RoleMember
}

// Rank is the role's position in the ordering. Unknown roles rank as member.
func (r Role) Rank() int {
	if rank, ok := roleRank[r]; ok {
		return rank
	}
	return roleRank[RoleMember]
}

// Valid reports whether r is one of the four known roles.
func (r Role) Valid() bool {
	_, ok := roleRank[r]
	return ok
}

// Allows reports whether a speaker with this role satisfies the given floor.
func (r Role) Allows(floor Role) bool {
	return r.Rank() >= floor.Rank()
}

// UnknownRole is the effective role of a voice we could not identify: it sits
// below every real role, so only intents marked min_role: any will run for it.
const UnknownRole = RoleAny
