package docparse

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSniff(t *testing.T) {
	cases := []struct {
		name string
		file string
		data []byte
		want string
	}{
		{"pdf magic", "x.bin", []byte("%PDF-1.4\n..."), "pdf"},
		{"pdf ext", "spec.pdf", []byte("garbage"), "pdf"},
		{"docx magic", "x.bin", buildDocx("<w:document/>"), "docx"},
		{"docx ext", "spec.docx", []byte("garbage"), "docx"},
		{"xlsx ext", "data.xlsx", buildXlsx(), "xlsx"},
		{"png magic", "x.bin", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0}, "image"},
		{"jpg ext", "mock.jpg", []byte("garbage"), "image"},
		{"txt ext", "prd.md", nil, "text"},
		{"text passthrough", "prd", []byte("plain text"), "text"},
		{"binary rejected", "blob", []byte{0x00, 0x01, 0x02}, "unsupported"},
		{"unknown zip rejected", "x.odt", []byte("PK\x03\x04"), "unsupported"},
	}
	for _, c := range cases {
		if got := Sniff(c.file, c.data); got != c.want {
			t.Errorf("%s: Sniff = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInferKind(t *testing.T) {
	cases := map[string]string{
		"product-prd.md":        "prd",
		"requirements.txt":      "prd",
		"design-system.pdf":     "design",
		"tech-design.docx":      "tech",
		"architecture-notes.md": "tech",
		"market-analysis.xlsx":  "market",
		"competitive.pdf":       "market",
		"random-notes.txt":      "other",
	}
	for in, want := range cases {
		if got := InferKind(in); got != want {
			t.Errorf("InferKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractTextPlain(t *testing.T) {
	got, err := ExtractText("prd.md", []byte("# Hello\n\nworld"))
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if got != "# Hello\n\nworld" {
		t.Errorf("ExtractText = %q", got)
	}
}

// buildZip builds an in-memory zip from name → content pairs.
func buildZip(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		fmt.Fprint(w, body)
	}
	zw.Close()
	return buf.Bytes()
}

// buildDocx assembles a minimal in-memory docx with word/document.xml.
func buildDocx(body string) []byte {
	return buildZip(map[string]string{"word/document.xml": body})
}

func TestExtractTextDocx(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t xml:space="preserve"> world</w:t></w:r></w:p>
    <w:p><w:r><w:t>Second paragraph</w:t></w:r></w:p>
  </w:body>
</w:document>`
	got, err := ExtractText("spec.docx", buildDocx(body))
	if err != nil {
		t.Fatalf("ExtractText docx: %v", err)
	}
	if !strings.Contains(got, "Hello world") || !strings.Contains(got, "Second paragraph") {
		t.Errorf("ExtractText docx = %q", got)
	}
	if strings.Count(got, "\n") < 1 {
		t.Errorf("expected paragraph newline, got %q", got)
	}
}

// buildXlsx assembles a minimal in-memory workbook with shared strings + one
// worksheet.
func buildXlsx() []byte {
	shared := `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2">
  <si><t>Name</t></si><si><t>Cost</t></si>
</sst>`
	sheet := `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
    <row r="2"><c r="A2" t="inlineStr"><is><t>Widget</t></is></c><c r="B2"><v>9.99</v></c></row>
  </sheetData>
</worksheet>`
	return buildZip(map[string]string{
		"xl/sharedStrings.xml":         shared,
		"xl/worksheets/sheet1.xml":     sheet,
		"xl/workbook.xml":              `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`,
		"[Content_Types].xml":          `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
	})
}

func TestExtractTextXlsx(t *testing.T) {
	got, err := ExtractText("data.xlsx", buildXlsx())
	if err != nil {
		t.Fatalf("ExtractText xlsx: %v", err)
	}
	if !strings.Contains(got, "Name\tCost") {
		t.Errorf("expected shared-string header row, got %q", got)
	}
	if !strings.Contains(got, "Widget\t9.99") {
		t.Errorf("expected inline/numeric row, got %q", got)
	}
}

func TestExtractTextImageRejected(t *testing.T) {
	if _, err := ExtractText("mock.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0}); err == nil {
		t.Errorf("expected an error for an image, got nil")
	}
}

// minimalPDF builds a single-page PDF with one text-showing operation, with a
// valid xref table so the reader can locate the objects.
func minimalPDF(text string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var offsets []int
	record := func(obj string) {
		offsets = append(offsets, b.Len())
		b.WriteString(obj)
	}
	record("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	record("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	record("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	record(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream))
	record("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")
	xrefPos := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefPos)
	return b.Bytes()
}

func TestExtractTextPDF(t *testing.T) {
	got, err := ExtractText("spec.pdf", minimalPDF("Hello PDF"))
	if err != nil {
		t.Fatalf("ExtractText pdf: %v", err)
	}
	if !strings.Contains(got, "Hello PDF") {
		t.Errorf("ExtractText pdf = %q, want to contain %q", got, "Hello PDF")
	}
}

func TestExtractTextPDFGarbageDoesNotPanic(t *testing.T) {
	if _, err := ExtractText("x.pdf", []byte("not a pdf at all")); err == nil {
		t.Errorf("expected an error for garbage pdf, got nil")
	}
}

// TestValidatePDFRejectsSizeBomb ensures a crafted /Size declaration does not
// reach the library (the xref-table allocation vector).
func TestValidatePDFRejectsSizeBomb(t *testing.T) {
	crafted := []byte("%PDF-1.4\n/Size 999999999999\n1 0 obj\n<< /Type /Catalog >>\nendobj\n")
	if err := validatePDF(crafted); err == nil {
		t.Errorf("expected a /Size rejection, got nil")
	}
}

func TestValidatePDFRejectsPageBomb(t *testing.T) {
	crafted := []byte("%PDF-1.4\n/Count 999999999999\n1 0 obj\n<< /Type /Pages >>\nendobj\n")
	if err := validatePDF(crafted); err == nil {
		t.Errorf("expected a /Count rejection, got nil")
	}
}
