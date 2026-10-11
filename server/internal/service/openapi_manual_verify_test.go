//go:build manual

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestManualRealOpenAPI is the "verify once by hand" acceptance check: fetch a
// real public OpenAPI document over the network and confirm a usable contract.
// Run with: go test -tags manual -run TestManualRealOpenAPI -v ./internal/service/
func TestManualRealOpenAPI(t *testing.T) {
	urls := []string{
		"https://petstore3.swagger.io/api/v3/openapi.json",
		"https://raw.githubusercontent.com/OAI/OpenAPI-Specification/main/examples/v3.0/petstore.json",
	}
	for _, u := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		body, err := FetchOpenAPIDocument(ctx, u)
		cancel()
		if err != nil {
			t.Logf("FETCH FAILED %s: %v", u, err)
			continue
		}
		d, err := ParseOpenAPIDocument(u, body)
		if err != nil {
			t.Logf("PARSE FAILED %s: %v", u, err)
			continue
		}
		lint := LintContract(d.ProviderContract)
		errs := 0
		for _, iss := range lint {
			if iss.Severity == "error" {
				errs++
			}
		}
		fmt.Printf("\n=== %s ===\nspec=%s title=%q digest=%s\nops=%d lint_errors=%d verify=%s\nbase_url=%s auth=%+v\n",
			u, d.SpecKind, d.Title, d.DocDigest,
			len(d.ProviderContract.Operations), errs, d.ProviderContract.VerifyOperation,
			d.ProviderContract.BaseURL, d.ProviderContract.Auth)
		blob, _ := json.MarshalIndent(d.HTTPDataSource, "", "  ")
		fmt.Printf("http_data_source=%s\n", blob)
		if errs > 0 {
			t.Errorf("%s: contract draft has %d lint errors: %v", u, errs, lint)
		}
		if len(d.ProviderContract.Operations) == 0 {
			t.Errorf("%s: no operations mapped", u)
		}
		return
	}
	t.Skip("no reachable public OpenAPI document (no network)")
}
