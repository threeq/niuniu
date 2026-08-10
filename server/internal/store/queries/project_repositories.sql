-- name: ListProjectRepositories :many
SELECT pr.project_id, pr.repository_id, pr.default_branch AS project_default_branch,
       r.id, r.name, r.path, r.git_remote, r.default_branch AS repo_default_branch,
       r.owner_type, r.owner_id, r.created_at, r.updated_at
FROM project_repositories pr
JOIN repositories r ON r.id = pr.repository_id
JOIN projects p ON p.id = pr.project_id
WHERE pr.project_id = ?
  AND r.owner_type = p.owner_type AND r.owner_id = p.owner_id
ORDER BY LOWER(r.name);

-- name: GetProjectRepository :one
SELECT project_id, repository_id, default_branch
FROM project_repositories
WHERE project_id = ? AND repository_id = ?;

-- name: InsertProjectRepository :exec
INSERT INTO project_repositories (project_id, repository_id, default_branch)
VALUES (?, ?, ?);

-- name: UpdateProjectRepositoryBranch :exec
UPDATE project_repositories
SET default_branch = ?
WHERE project_id = ? AND repository_id = ?;

-- name: DeleteProjectRepository :exec
DELETE FROM project_repositories
WHERE project_id = ? AND repository_id = ?;

-- name: DeleteCrossOwnerProjectRepositoriesByProject :execrows
-- Used by transfer-resource (Project transferred). Deletes every binding
-- whose repo owner doesn't match the new project owner.
DELETE FROM project_repositories
WHERE project_id = ?
  AND repository_id IN (
    SELECT id FROM repositories
    WHERE id = project_repositories.repository_id
      AND (owner_type != ? OR owner_id != ?)
  );

-- name: DeleteCrossOwnerProjectRepositoriesByRepo :execrows
-- Used by transfer-resource (Repository transferred). Deletes every binding
-- whose project owner doesn't match the repo's new owner.
DELETE FROM project_repositories
WHERE repository_id = ?
  AND project_id IN (
    SELECT id FROM projects
    WHERE id = project_repositories.project_id
      AND (owner_type != ? OR owner_id != ?)
  );

-- name: GetRepositoryOwner :one
SELECT owner_type, owner_id FROM repositories WHERE id = ?;
