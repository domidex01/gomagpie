package clean

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

// buildPDF assembles a minimal valid PDF from per-page text lines,
// computing xref offsets programmatically — no binary blobs in git.
// Object layout: 1 catalog, 2 pages tree, 3+2i page i, 4+2i contents i,
// last font. An empty string yields a page with no text (scan-PDF shape).
func buildPDF(pageTexts ...string) []byte {
	type obj struct {
		num int
		raw string
	}
	var objs []obj
	fontNum := 3 + 2*len(pageTexts)
	kids := make([]string, len(pageTexts))
	for i, text := range pageTexts {
		pn, cn := 3+2*i, 4+2*i
		kids[i] = fmt.Sprintf("%d 0 R", pn)
		stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
		objs = append(objs,
			obj{pn, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >>", cn, fontNum)},
			obj{cn, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)},
		)
	}
	objs = append(objs,
		obj{1, "<< /Type /Catalog /Pages 2 0 R >>"},
		obj{2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pageTexts))},
		obj{fontNum, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"},
	)
	var buf bytes.Buffer
	offsets := map[int]int{}
	buf.WriteString("%PDF-1.4\n")
	for _, o := range objs {
		offsets[o.num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", o.num, o.raw)
	}
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", fontNum+1)
	for n := 1; n <= fontNum; n++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", fontNum+1, xrefOff)
	return buf.Bytes()
}

func TestPDFToMarkdownPages(t *testing.T) {
	body := buildPDF("Hello from page one", "Second page text")
	md, title, err := pdfToMarkdown(body, "https://example.com/docs/annual-report.pdf")
	if err != nil {
		t.Fatalf("pdfToMarkdown: %v", err)
	}
	if title != "annual-report" {
		t.Errorf("title = %q, want annual-report", title)
	}
	for _, want := range []string{"## Page 1", "Hello from page one", "## Page 2", "Second page text"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "## Page 3") {
		t.Error("unexpected Page 3 section")
	}
}

func TestPDFTitleFallback(t *testing.T) {
	if _, title, err := pdfToMarkdown(buildPDF("x"), "https://example.com/"); err != nil || title != "document" {
		t.Errorf("title = %q, err = %v; want document, nil", title, err)
	}
}

func TestIsPDFContentTypeTable(t *testing.T) {
	cases := map[string]bool{
		"application/pdf":                 true,
		"Application/PDF":                 true,
		"application/pdf; charset=binary": true,
		"text/html":                       false,
		"":                                false,
	}
	for ct, want := range cases {
		if got := IsPDFContentType(ct); got != want {
			t.Errorf("IsPDFContentType(%q) = %v, want %v", ct, got, want)
		}
	}
}

// The encrypted-PDF sentinel mapping is unit-proven (building a real
// /Encrypt dict fixture is disproportionate): malformed stays malformed,
// the lib's ErrInvalidPassword wraps ErrPDFEncrypted.
func TestPDFReadErrMapping(t *testing.T) {
	err := pdfReadErr(pdf.ErrInvalidPassword, "https://example.com/secret.pdf")
	if !errors.Is(err, ErrPDFEncrypted) {
		t.Errorf("ErrInvalidPassword → %v, want ErrPDFEncrypted wrap", err)
	}
	err = pdfReadErr(errors.New("boom"), "https://example.com/x.pdf")
	if errors.Is(err, ErrPDFEncrypted) {
		t.Errorf("generic error → %v, must not be ErrPDFEncrypted", err)
	}
}

func TestCleanPDFEmptyIsQualityEmpty(t *testing.T) {
	out, err := Clean(t.Context(), RawPage{HTML: buildPDF(""), URL: "https://example.com/scan.pdf", FinalURL: "https://example.com/scan.pdf"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if out.Quality != IssueEmpty {
		t.Errorf("Quality = %q, want empty (text-less scan PDF)", out.Quality)
	}
}

// Rich PDF mentioning "captcha" in its text must stay clean: binary-noise
// marker rules are dead for PDFs, and 200+ words beats the thin guard.
func TestCleanPDFRichMentioningCaptcha(t *testing.T) {
	text := strings.Repeat("quarterly revenue grew across every region this year ", 30) + " note about captcha handling"
	out, err := Clean(t.Context(), RawPage{HTML: buildPDF(text), URL: "https://example.com/rich.pdf", FinalURL: "https://example.com/rich.pdf"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if out.Quality != IssueNone {
		t.Errorf("Quality = %q, want none (rich PDF mentioning captcha)", out.Quality)
	}
}

func TestCleanPDFMalformedFailsLoud(t *testing.T) {
	for name, body := range map[string][]byte{
		"magic-no-eof": []byte("%PDF-1.4\n1 0 obj garbage without xref or eof"),
		"ct-only-junk": []byte("plainly not a pdf at all"),
	} {
		_, err := Clean(t.Context(), RawPage{HTML: body, URL: "https://example.com/bad.pdf", ContentType: "application/pdf"})
		if err == nil {
			t.Errorf("%s: Clean succeeded, want loud error", name)
		}
	}
}

func TestCleanPDFTitleAndMarkdown(t *testing.T) {
	out, err := Clean(t.Context(), RawPage{HTML: buildPDF("Totally real content here"), URL: "https://example.com/guide.pdf", FinalURL: "https://example.com/guide.pdf"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if out.Title != "guide" {
		t.Errorf("Title = %q, want guide", out.Title)
	}
	if !strings.Contains(out.Markdown, "## Page 1") || !strings.Contains(out.Markdown, "Totally real content here") {
		t.Errorf("markdown = %q, want page section + text", out.Markdown)
	}
	if out.StructuredData != nil {
		t.Error("PDFs must not carry sidecar structured data")
	}
}
