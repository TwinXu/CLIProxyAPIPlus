package api

import (
	"bytes"
	"strings"
	"testing"
)

func TestInjectKiroCreditScriptBeforeBody(t *testing.T) {
	html := []byte("<html><body><div id=\"root\"></div></body></html>")

	out := injectKiroCreditScript(html)

	if !bytes.Contains(out, []byte(kiroCreditScriptMarker)) {
		t.Fatalf("injected html missing marker: %s", string(out))
	}
	if bytes.Index(out, []byte(kiroCreditScriptMarker)) > bytes.Index(out, []byte("</body>")) {
		t.Fatalf("script was not injected before closing body: %s", string(out))
	}
}

func TestInjectKiroCreditScriptIdempotent(t *testing.T) {
	html := injectKiroCreditScript([]byte("<html><body></body></html>"))

	out := injectKiroCreditScript(html)

	if bytes.Count(out, []byte(kiroCreditScriptMarker)) != 1 {
		t.Fatalf("script marker count = %d, want 1", bytes.Count(out, []byte(kiroCreditScriptMarker)))
	}
}

func TestKiroCreditScriptSupportsLegacyAndModernAuthCards(t *testing.T) {
	required := []string{
		"AuthFileCard-module__card___",
		"AuthFileCard-module__typeBadge___",
		"AuthFileCard-module__fileName___",
		"AuthFileCard-module__account___",
		"AuthFileCard-module__metaRow___",
		"AuthFileCard-module__metaDivider___",
		"AuthFileCard-module__metaMetricLabel___",
		"AuthFilesPage-module__fileCard___",
		"AuthFilesPage-module__metaItem___",
		`if (itemClass) {`,
		`document.createElement("span")`,
		`aria-hidden="true">·</span>`,
		`element.getAttribute("title")`,
		`replace(/\.json$/, "")`,
		`findAuthFileForCard(card, files)`,
		`file?.type || file?.provider`,
	}
	for _, value := range required {
		if !strings.Contains(kiroCreditScript, value) {
			t.Errorf("credit script missing compatibility marker %q", value)
		}
	}
}
