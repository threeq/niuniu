package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// ErrorResponse represents an API error response
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// Error is an alias for ErrorResponse used in Swagger annotations.
type Error = ErrorResponse

// ErrorDetail contains detailed information about an error
type ErrorDetail struct {
	Code    string      `json:"code" example:"PROJECT_NOT_FOUND"`
	Message string      `json:"message" example:"Project not found"`
	Details interface{} `json:"details,omitempty"`
}

func RespondError(c *gin.Context, status int, code, message string) {
	c.JSON(status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}

func RespondErrorWithDetails(c *gin.Context, status int, code, message string, details interface{}) {
	c.JSON(status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message, Details: details},
	})
}

func NotFound(c *gin.Context, resource string) {
	RespondError(c, http.StatusNotFound, resource+"_NOT_FOUND", resource+" not found")
}

func BadRequest(c *gin.Context, message string) {
	RespondError(c, http.StatusBadRequest, "BAD_REQUEST", message)
}

func InternalError(c *gin.Context, err error) {
	slog.Error("internal error", "path", c.Request.URL.Path, "error", err)
	RespondError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
}

// Conflict responds with 409 Conflict.
func Conflict(c *gin.Context, message string) {
	RespondError(c, http.StatusConflict, "CONFLICT", message)
}

// writeAuthzError maps Authz errors to appropriate HTTP responses.
func writeAuthzError(c *gin.Context, err error) {
	switch err {
	case service.ErrNotFound:
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case service.ErrForbidden:
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
