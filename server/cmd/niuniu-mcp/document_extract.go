package main

// Document text extraction now lives in internal/docextract so the
// knowledge-base ingest path can share one implementation (previously this file
// held the only copy, reachable solely from this binary). These aliases keep the
// existing call sites and tests in this package unchanged.

import "github.com/niuniu-dev/niuniu/internal/docextract"

type (
	docSegment      = docextract.Segment
	extractResult   = docextract.Result
	extractFallback = docextract.Fallback
)

func detectFormat(path string) string { return docextract.DetectFormat(path) }

func extractDocument(path string, data []byte, pages []int) (*extractResult, error) {
	return docextract.Extract(path, data, pages)
}

func extractDocx(data []byte) (*extractResult, error) { return docextract.ExtractDocx(data) }

func extractPDFInfo(data []byte) (int, error) { return docextract.PDFPageCount(data) }

func ooxmlUnitLabels(format string, data []byte) (string, []string, error) {
	return docextract.UnitLabels(format, data)
}

func parsePageSpec(spec string, max int) ([]int, error) { return docextract.ParsePageSpec(spec, max) }
