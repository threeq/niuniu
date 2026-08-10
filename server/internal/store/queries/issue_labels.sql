-- name: AddIssueLabel :exec
INSERT OR IGNORE INTO issue_labels (issue_id, label_id) VALUES (?, ?);

-- name: RemoveIssueLabel :exec
DELETE FROM issue_labels WHERE issue_id = ? AND label_id = ?;

-- name: ClearIssueLabels :exec
DELETE FROM issue_labels WHERE issue_id = ?;

-- name: ListIssueLabelIDs :many
SELECT label_id FROM issue_labels WHERE issue_id = ?;
