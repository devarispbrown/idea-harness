// Package docparse extracts plain text from uploaded context documents.
// Markdown/plain text/CSV pass through verbatim; .docx and .xlsx are unzipped
// and their XML text extracted; .pdf text is extracted with a pure-Go PDF
// reader after a bounded pre-validation pass. Images are detected but not
// text-extractable (they are handled by the vision path, not here).
package docparse

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// MaxExtractedBytes caps the extracted text per document (mirrors the 2 MiB
// upload cap, §48 input budget). Extraction is deterministic and bounded.
const MaxExtractedBytes = 2 << 20

// PDF pre-validation ceilings (§48). A PDF is rejected before the library reads
// it if its declared object/page counts or per-stream decompressed size exceed
// these bounds — a <=2 MiB file must never expand into a multi-GB allocation.
const (
	maxPDFObjects      = 1_000_000
	maxPDFPages        = 50_000
	maxPDFStreams      = 4096
	maxPDFStreamExpand = 8 << 20 // per-stream decompression ceiling
)

// InferKind guesses the semantic context-doc kind (prd/design/tech/market/
// other) from a filename, used when the uploader does not specify one. It is
// the single canonical mapping shared by the CLI and the daemon.
func InferKind(filename string) string {
	base := strings.ToLower(filepath.Base(filename))
	switch {
	case strings.Contains(base, "prd"), strings.Contains(base, "requirements"):
		return "prd"
	case strings.Contains(base, "tech"), strings.Contains(base, "architecture"), strings.Contains(base, "spec"):
		return "tech"
	case strings.Contains(base, "design"):
		return "design"
	case strings.Contains(base, "market"), strings.Contains(base, "competitive"), strings.Contains(base, "analysis"):
		return "market"
	default:
		return "other"
	}
}

// Sniff returns the concrete file format: "pdf", "docx", "xlsx", "image",
// "text", or "unsupported". Magic bytes win over the extension; unknown binary
// input is classified "unsupported" so it is never ingested as garbage text.
// Images are classified by MAGIC BYTES ONLY — an extension-only match is NOT
// treated as an image, so a text file renamed to .png/.jpg still flows through
// the text path (credential-swept + redacted) instead of bypassing it.
func Sniff(filename string, data []byte) string {
	if len(data) >= 5 && string(data[:5]) == "%PDF-" {
		return "pdf"
	}
	if len(data) >= 2 && data[0] == 'P' && data[1] == 'K' {
		return zipFormat(data)
	}
	if isJPEG(data) || isPNG(data) || isGIF(data) || isWebP(data) {
		return "image"
	}
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "pdf"
	case strings.HasSuffix(lower, ".docx"):
		return "docx"
	case strings.HasSuffix(lower, ".xlsx"):
		return "xlsx"
	case isTextExtension(lower):
		return "text"
	}
	if isText(data) {
		return "text"
	}
	return "unsupported"
}

// ImageMediaType returns the MIME type for a recognized image (by magic bytes)
// or "" when data is not a JPEG/PNG/GIF/WebP. Used as the media-type allowlist
// for vision requests instead of content-sniffing.
func ImageMediaType(data []byte) string {
	switch {
	case isJPEG(data):
		return "image/jpeg"
	case isPNG(data):
		return "image/png"
	case isGIF(data):
		return "image/gif"
	case isWebP(data):
		return "image/webp"
	default:
		return ""
	}
}

// ExtractText returns the plain-text body of a document. Unsupported formats
// (including images, which need the vision path) return an error so the daemon
// rejects them rather than ingesting binary garbage.
func ExtractText(filename string, data []byte) (string, error) {
	switch Sniff(filename, data) {
	case "pdf":
		return extractPDF(data)
	case "docx":
		return extractDocx(data)
	case "xlsx":
		return extractXlsx(data)
	case "text":
		return string(data), nil
	case "image":
		return "", fmt.Errorf("image uploads are handled by the vision path, not text extraction")
	default:
		return "", fmt.Errorf("unsupported file format")
	}
}

// zipFormat inspects a zip container to tell docx/xlsx apart from any other
// archive (odt, jar, xlsm, …), which is rejected.
func zipFormat(data []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "unsupported"
	}
	hasDocx, hasXlsx := false, false
	for _, f := range zr.File {
		switch {
		case f.Name == "word/document.xml":
			hasDocx = true
		case f.Name == "xl/workbook.xml" || f.Name == "xl/sharedStrings.xml" || strings.HasPrefix(f.Name, "xl/worksheets/"):
			hasXlsx = true
		}
	}
	switch {
	case hasDocx:
		return "docx"
	case hasXlsx:
		return "xlsx"
	default:
		return "unsupported"
	}
}

func isJPEG(d []byte) bool {
	return len(d) >= 3 && d[0] == 0xFF && d[1] == 0xD8 && d[2] == 0xFF
}

func isPNG(d []byte) bool {
	return len(d) >= 8 && bytes.Equal(d[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
}

func isGIF(d []byte) bool {
	return len(d) >= 6 && (string(d[:6]) == "GIF87a" || string(d[:6]) == "GIF89a")
}

func isWebP(d []byte) bool {
	return len(d) >= 12 && string(d[:4]) == "RIFF" && string(d[8:12]) == "WEBP"
}

func isTextExtension(lower string) bool {
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"),
		strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".text"),
		strings.HasSuffix(lower, ".csv"), strings.HasSuffix(lower, ".tsv"),
		strings.HasSuffix(lower, ".json"), strings.HasSuffix(lower, ".yaml"),
		strings.HasSuffix(lower, ".yml"), strings.HasSuffix(lower, ".log"),
		strings.HasSuffix(lower, ".rst"), strings.HasSuffix(lower, ".adoc"):
		return true
	}
	return false
}

// isText reports whether data looks like plain text (UTF-8, no NUL bytes).
func isText(data []byte) bool {
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	return utf8.Valid(data)
}

// validatePDF bounds a PDF before the library allocates from its declared
// structure. It is defense-in-depth: the two attack vectors are a crafted /Size
// xref allocation and a FlateDecode stream that expands far beyond the input.
func validatePDF(data []byte) error {
	if n := maxIntAfter(data, []byte("/Size")); n > maxPDFObjects {
		return fmt.Errorf("pdf: declared object count %d exceeds limit %d", n, maxPDFObjects)
	}
	if n := maxIntAfter(data, []byte("/Count")); n > maxPDFPages {
		return fmt.Errorf("pdf: declared page count %d exceeds limit %d", n, maxPDFPages)
	}
	blocks := findStreamBlocks(data)
	if len(blocks) > maxPDFStreams {
		return fmt.Errorf("pdf: %d streams exceeds limit %d", len(blocks), maxPDFStreams)
	}
	for _, b := range blocks {
		n, expanded := boundedInflate(b)
		if expanded {
			return fmt.Errorf("pdf: a stream decompresses beyond %d bytes", maxPDFStreamExpand)
		}
		_ = n
	}
	return nil
}

// maxIntAfter returns the largest integer that appears after any occurrence of
// key (with optional whitespace or "/" between). Used to bound /Size and /Count.
func maxIntAfter(data []byte, key []byte) int64 {
	var max int64
	rest := data
	for {
		i := bytes.Index(rest, key)
		if i < 0 {
			break
		}
		j := i + len(key)
		for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t' || rest[j] == '\r' || rest[j] == '\n' || rest[j] == '/') {
			j++
		}
		k := j
		for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
			k++
		}
		if k > j {
			if n, err := strconv.ParseInt(string(rest[j:k]), 10, 64); err == nil && n > max {
				max = n
			}
		}
		rest = rest[j:]
	}
	return max
}

// findStreamBlocks returns the raw payloads between `stream` and `endstream`
// keywords. It is a heuristic for the inflate probe (not a full parser): false
// splits are harmless because a block that is not a deflate stream simply fails
// to inflate.
func findStreamBlocks(data []byte) [][]byte {
	var blocks [][]byte
	rest := data
	for {
		i := bytes.Index(rest, []byte("stream"))
		if i < 0 {
			break
		}
		j := i + len("stream")
		if j < len(rest) && rest[j] == '\r' {
			j++
		}
		if j < len(rest) && rest[j] == '\n' {
			j++
		}
		k := bytes.Index(rest[j:], []byte("endstream"))
		if k < 0 {
			break
		}
		blocks = append(blocks, rest[j:j+k])
		rest = rest[j+k+len("endstream"):]
	}
	return blocks
}

// boundedInflate inflates a candidate stream (zlib, then raw deflate) and
// reports whether it expands beyond maxPDFStreamExpand. Non-deflate payloads
// (uncompressed or LZW) fail to inflate and report (0, false).
func boundedInflate(data []byte) (int, bool) {
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		n, _ := io.Copy(io.Discard, io.LimitReader(zr, maxPDFStreamExpand+1))
		zr.Close()
		return int(n), n > maxPDFStreamExpand
	}
	fr := flate.NewReader(bytes.NewReader(data))
	n, _ := io.Copy(io.Discard, io.LimitReader(fr, maxPDFStreamExpand+1))
	fr.Close()
	return int(n), n > maxPDFStreamExpand
}

// extractPDF validates, then pulls the concatenated plain text from every page.
// A panic from the PDF library is recovered into an error so a malformed file
// can never crash the daemon. (Validation bounds allocations; recover is a
// last-resort guard for any parser bug validation missed.)
func extractPDF(data []byte) (out string, err error) {
	if err := validatePDF(data); err != nil {
		return "", err
	}
	defer func() {
		if r := recover(); r != nil {
			out, err = "", fmt.Errorf("pdf parse panic: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf: %w", err)
	}
	if r.NumPage() > maxPDFPages {
		return "", fmt.Errorf("pdf: page count %d exceeds limit %d", r.NumPage(), maxPDFPages)
	}
	reader, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("pdf: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxExtractedBytes))
	if err != nil {
		return "", fmt.Errorf("pdf: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// extractDocx unzips the container and extracts text from word/document.xml.
func extractDocx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("docx: %w", err)
	}
	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", fmt.Errorf("docx: missing word/document.xml")
	}
	rc, err := docFile.Open()
	if err != nil {
		return "", fmt.Errorf("docx: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, MaxExtractedBytes))
	if err != nil {
		return "", fmt.Errorf("docx: %w", err)
	}
	return docxXMLToText(raw), nil
}

// docxXMLToText walks the WordprocessingML document and joins the text of each
// <w:t> run, inserting a newline at each paragraph end and whitespace for
// explicit tabs/breaks. Adjacent <w:t> runs are concatenated (which also
// re-joins a credential split across runs, aiding the ingest-time sweep).
func docxXMLToText(raw []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var b strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "p":
				b.WriteString("\n")
			case "tab":
				b.WriteString("\t")
			case "br", "cr":
				b.WriteString("\n")
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
		case xml.CharData:
			if inText {
				b.WriteString(string(t))
			}
		}
	}
	lines := strings.Split(b.String(), "\n")
	var out []string
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// extractXlsx unzips the workbook and renders each worksheet's cell values as
// tab-separated rows, resolving shared strings.
func extractXlsx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("xlsx: %w", err)
	}
	shared, err := xlsxSharedStrings(zr)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "xl/worksheets/sheet") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(rc, MaxExtractedBytes))
		rc.Close()
		if err != nil {
			continue
		}
		out.WriteString(xlsxSheetToText(raw, shared))
		out.WriteString("\n")
		if out.Len() >= MaxExtractedBytes {
			break
		}
	}
	return truncateBytes(out.String(), MaxExtractedBytes), nil
}

// xlsxSharedStrings returns the shared-string table, or nil when absent.
func xlsxSharedStrings(zr *zip.Reader) ([]string, error) {
	var f *zip.File
	for _, x := range zr.File {
		if x.Name == "xl/sharedStrings.xml" {
			f = x
			break
		}
	}
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("xlsx: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, MaxExtractedBytes))
	if err != nil {
		return nil, fmt.Errorf("xlsx: %w", err)
	}
	return parseSharedStrings(raw), nil
}

func parseSharedStrings(raw []byte) []string {
	var out []string
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var cur strings.Builder
	inT := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, cur.String())
			}
		case xml.CharData:
			if inT {
				cur.WriteString(string(t))
			}
		}
	}
	return out
}

func xlsxSheetToText(raw []byte, shared []string) string {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var row []string
	var out []string
	var cellVal strings.Builder
	var cellIsShared bool
	inV, inT := false, false

	flushCell := func() {
		v := cellVal.String()
		if cellIsShared {
			if idx, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && idx >= 0 && idx < len(shared) {
				v = shared[idx]
			} else {
				v = ""
			}
		}
		row = append(row, v)
		cellVal.Reset()
		cellIsShared = false
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				cellIsShared = false
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellIsShared = a.Value == "s"
					}
				}
			case "v":
				inV = true
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				flushCell()
			case "row":
				for len(row) > 0 && row[len(row)-1] == "" {
					row = row[:len(row)-1]
				}
				out = append(out, strings.Join(row, "\t"))
				row = nil
			}
		case xml.CharData:
			if inV || inT {
				cellVal.WriteString(string(t))
			}
		}
	}
	return strings.Join(out, "\n")
}

// truncateBytes caps s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n…[truncated]"
}
