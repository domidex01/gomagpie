package clean

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ErrPDFEncrypted marks password-protected PDFs; edges match it with
// errors.Is. No new Issue value — a PDF that can't be read is a hard
// error, never empty-markdown success.
var ErrPDFEncrypted = errors.New("clean: encrypted PDF (password required)")

// pdfMagic is the PDF header prefix (spec E2 detection).
var pdfMagic = []byte("%PDF-")

// isPDFBody reports the %PDF- magic prefix.
func isPDFBody(b []byte) bool { return bytes.HasPrefix(b, pdfMagic) }

// IsPDFContentType reports an application/pdf Content-Type (parameters
// like "; charset=..." tolerated).
func IsPDFContentType(ct string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/pdf")
}

// pdfToMarkdown extracts one "## Page N" section per non-empty page.
// Title derives from the URL path stem. Panics (hostile/truncated input
// past NewReader's own recover) become typed errors, never a crash.
func pdfToMarkdown(body []byte, sourceURL string) (md, title string, err error) {
	defer func() {
		if r := recover(); r != nil {
			md, title, err = "", "", fmt.Errorf("clean: malformed PDF %s: %v", sourceURL, r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", "", pdfReadErr(err, sourceURL)
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, perr := p.GetPlainText(nil)
		if perr != nil {
			return "", "", fmt.Errorf("clean: PDF page %d of %s: %w", i, sourceURL, perr)
		}
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		fmt.Fprintf(&b, "## Page %d\n\n%s\n\n", i, text)
	}
	return b.String(), pdfTitle(sourceURL), nil
}

// pdfReadErr maps lib errors to our typed ones: encrypted → ErrPDFEncrypted
// (errors.Is-able at the edges), anything else → malformed wrap.
func pdfReadErr(err error, sourceURL string) error {
	if errors.Is(err, pdf.ErrInvalidPassword) {
		return fmt.Errorf("clean: %s: %w", sourceURL, ErrPDFEncrypted)
	}
	return fmt.Errorf("clean: malformed PDF %s: %w", sourceURL, err)
}

// pdfTitle derives the document title from the URL path stem
// ("report.pdf" → "report"), falling back to "document".
func pdfTitle(sourceURL string) string {
	name := "document"
	if u, err := url.Parse(sourceURL); err == nil && u.Path != "" && u.Path != "/" {
		if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
			name = base
		}
	}
	return strings.TrimSuffix(name, ".pdf")
}
