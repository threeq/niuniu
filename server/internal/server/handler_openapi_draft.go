package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
)

// proxyOpenAPIDraft handles
// POST /api/me/external-proxy/providers/:id/openapi-draft[?refresh=true]
// (and the /mcp twin, so an agent can draft an integration from a provider's
// openapi_url without a human driving the UI).
//
// It fetches the provider's openapi_url through the SSRF-guarded client, parses
// the document and returns BOTH draft shapes — a ProviderContract and an HTTP
// data source config — inside an IntegrationDocDraft. The route is read-only by
// construction: it reads an existing provider row, never mutates it, and the
// returned draft grants nothing. Publishing or widening the suggested scope is
// the draft pipeline's job and requires an explicit human confirmation; this
// endpoint cannot apply anything.
//
// The response is cached by source URL for a short TTL; ?refresh=true forces a
// re-fetch (the manual refresh entry).
func (s *Server) proxyOpenAPIDraft(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}

	id, ok := providerPathID(c)
	if !ok {
		return
	}
	prov, err := s.externalProviderSvc.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found"}})
		return
	}
	if strings.TrimSpace(prov.OpenAPIURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    "NO_OPENAPI_URL",
			"message": "provider has no openapi_url; set one before generating a draft",
		}})
		return
	}

	refresh := c.Query("refresh") == "true" || c.Query("refresh") == "1"

	draft, err := service.DefaultOpenAPIDraftService().Generate(c.Request.Context(), prov.OpenAPIURL, refresh)
	if err != nil {
		// The document could not be fetched or is unusable — an upstream
		// problem, reported as 502 with the reason.
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"code":    "OPENAPI_DRAFT_FAILED",
			"message": err.Error(),
		}})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"provider": gin.H{"id": prov.ID, "name": prov.Name, "label": prov.Label},
			"draft":    draft,
		},
	})
}
