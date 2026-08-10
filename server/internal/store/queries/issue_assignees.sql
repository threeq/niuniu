-- name: AddIssueAssignee :exec
INSERT OR IGNORE INTO issue_assignees (issue_id, user_id) VALUES (?, ?);

-- name: ClearIssueAssignees :exec
DELETE FROM issue_assignees WHERE issue_id = ?;

-- name: ListIssueAssignees :many
SELECT u.id, u.username, u.display_name
FROM issue_assignees ia
JOIN users u ON u.id = ia.user_id
WHERE ia.issue_id = ?
ORDER BY u.display_name, u.username;

-- name: ListIssueAssigneesByIssueIDs :many
SELECT ia.issue_id, u.id, u.username, u.display_name
FROM issue_assignees ia
JOIN users u ON u.id = ia.user_id
WHERE ia.issue_id IN (sqlc.slice('issue_ids'))
ORDER BY ia.issue_id, u.display_name;

-- name: DeleteIssueAssigneesForUserInOrg :execrows
DELETE FROM issue_assignees
WHERE user_id = ?
  AND issue_id IN (
    SELECT i.id FROM issues i
    JOIN columns c  ON c.id = i.column_id
    JOIN projects p ON p.id = c.project_id
    WHERE p.owner_type = 'org' AND p.owner_id = ?
  );

-- name: ListAffectedIssueIDsForUserInOrg :many
-- Used to populate audit before the cascade DELETE. No LIMIT - the audit
-- payload should reflect the full set; the bottleneck is the DELETE, not the
-- TEXT column. A user with thousands of assignments is rare and the cleanup
-- gets logged exactly once on member removal.
SELECT i.id FROM issues i
JOIN issue_assignees ia ON ia.issue_id = i.id
JOIN columns c          ON c.id = i.column_id
JOIN projects p         ON p.id = c.project_id
WHERE ia.user_id = ?
  AND p.owner_type = 'org' AND p.owner_id = ?
ORDER BY i.id;

