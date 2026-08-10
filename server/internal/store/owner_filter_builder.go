package store

// BuildOwnerFilter returns a WHERE clause fragment and args for owner-scoped
// raw SQL queries. Intended for use with *store.DB which rewrites ? placeholders
// for PostgreSQL automatically.
//
// If ownerType is empty, returns a no-op filter ("1=1") with nil args so the
// query returns all rows regardless of owner. Otherwise restricts to the given
// (ownerType, ownerID) pair.
//
// Usage:
//
//	filter, args := store.BuildOwnerFilter(ownerType, ownerID)
//	query := "SELECT * FROM projects WHERE " + filter + " ORDER BY created_at DESC"
//	rows, err := db.QueryContext(ctx, query, args...)
func BuildOwnerFilter(ownerType string, ownerID int64) (sql string, args []interface{}) {
	if ownerType == "" {
		return "1=1", nil
	}
	return "owner_type = ? AND owner_id = ?", []interface{}{ownerType, ownerID}
}
