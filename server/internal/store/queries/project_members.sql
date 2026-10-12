-- name: AddProjectMember :exec
INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, ?);

-- name: GetProjectMember :one
SELECT * FROM project_members WHERE project_id = ? AND user_id = ?;

-- name: ListProjectMembers :many
SELECT m.*, u.username, u.display_name
FROM project_members m
JOIN users u ON u.id = m.user_id
WHERE m.project_id = ?
ORDER BY u.username;

-- name: ListProjectIDsForUser :many
SELECT project_id FROM project_members WHERE user_id = ?;

-- name: RemoveProjectMember :exec
DELETE FROM project_members WHERE project_id = ? AND user_id = ?;

-- name: DeleteProjectMembersForUserInOrg :execrows
DELETE FROM project_members
WHERE user_id = ?
  AND project_id IN (SELECT id FROM projects WHERE owner_type = 'org' AND owner_id = ?);
