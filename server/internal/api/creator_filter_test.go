package api

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseCreatorParam(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mkCtx := func(q string) *gin.Context {
		req := httptest.NewRequest("GET", "/?"+q, nil)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		// Ensure URL.Query() works.
		_, _ = url.ParseQuery(q)
		return c
	}

	cases := []struct {
		name        string
		q           string
		callerID    int64
		wantNil     bool
		wantValue   int64
		wantErr     bool
	}{
		{"empty returns nil", "", 7, true, 0, false},
		{"me returns caller", "created_by=me", 7, false, 7, false},
		{"all returns nil", "created_by=all", 7, true, 0, false},
		{"explicit id", "created_by=42", 7, false, 42, false},
		{"invalid string", "created_by=abc", 7, false, 0, true},
		{"negative rejected", "created_by=-1", 7, false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := mkCtx(tc.q)
			got, err := parseCreatorParam(ctx, tc.callerID)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if tc.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", *got)
				}
				return
			}
			if got == nil {
				t.Errorf("expected %d, got nil", tc.wantValue)
				return
			}
			if *got != tc.wantValue {
				t.Errorf("got %d, want %d", *got, tc.wantValue)
			}
		})
	}
}
