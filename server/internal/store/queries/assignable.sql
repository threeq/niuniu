-- name: ListAssignableUsersForOrg :many
SELECT u.id, u.username, u.display_name, m.role
FROM org_members m
JOIN users u ON u.id = m.user_id
WHERE m.org_id = ?
ORDER BY u.display_name, u.username;

-- name: GetUserBasic :one
SELECT id, username, display_name FROM users WHERE id = ?;
