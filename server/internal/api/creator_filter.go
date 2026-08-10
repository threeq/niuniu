package api

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
)

// parseCreatorParam decodes the optional ?created_by= query param.
//
//	""             -> (nil, nil)            no filter
//	"me"           -> (&callerID, nil)      filter to caller's own
//	"all"          -> (nil, nil)            explicit all (alias for "")
//	"<positive int>" -> (&id, nil)
//	otherwise      -> (nil, error)
//
// Returning *int64 (vs an "ok" bool + int64) lets callers pass
// straight into WorkspaceListFilter.CreatorID.
func parseCreatorParam(c *gin.Context, callerID int64) (*int64, error) {
	raw := c.Query("created_by")
	switch raw {
	case "":
		return nil, nil
	case "all":
		return nil, nil
	case "me":
		id := callerID
		return &id, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, errors.New("invalid created_by: must be 'me', 'all', or a positive integer")
	}
	if id <= 0 {
		return nil, errors.New("invalid created_by: must be positive")
	}
	return &id, nil
}
