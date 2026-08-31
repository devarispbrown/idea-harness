package security

import "regexp"

var (
	scriptRe  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	jsURLRe   = regexp.MustCompile(`(?i)\b(href|src)\s*=\s*["']\s*javascript:[^"']*["']`)
	dataURLRe = regexp.MustCompile(`(?i)\b(href|src)\s*=\s*["']\s*data:[^"']*["']`)
	mdLinkRe  = regexp.MustCompile(`(?i)\]\(\s*(javascript|data):[^)]*\)`)
	tagRe     = regexp.MustCompile(`(?s)<(/?)([a-zA-Z][a-zA-Z0-9]*)(\s[^<>]*)?>`)
)

// SanitizeMarkdown neutralizes raw HTML and dangerous URLs in artifact bodies
// before they are served to the UI (§15, stored-XSS containment). The frontend
// additionally renders markdown with a renderer that escapes raw HTML.
func SanitizeMarkdown(s string) string {
	s = scriptRe.ReplaceAllString(s, "")
	s = jsURLRe.ReplaceAllString(s, `$1="#"`)
	s = dataURLRe.ReplaceAllString(s, `$1="#"`)
	s = mdLinkRe.ReplaceAllString(s, `](#)`)
	// Escape any remaining raw HTML tags so they render as text, not markup.
	s = tagRe.ReplaceAllString(s, "&lt;$1$2$3&gt;")
	return s
}
