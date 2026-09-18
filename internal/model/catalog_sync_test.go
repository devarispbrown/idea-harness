package model

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The web model picker keeps its own copy of DefaultModels
// (web/src/modelCatalog.ts, CURATED_MODELS) to order the live listings. The
// copy drifts silently when only one side is edited, and the picker then
// leads with a model the CLI and API no longer default to.
func TestWebCuratedModelsMatchTheCatalog(t *testing.T) {
	src, err := os.ReadFile("../../web/src/modelCatalog.ts")
	if err != nil {
		t.Skipf("web source not present: %v", err)
	}
	block := regexp.MustCompile(`(?s)CURATED_MODELS[^=]*=\s*\{(.*?)\n\};`).FindSubmatch(src)
	if block == nil {
		t.Fatal("CURATED_MODELS not found in web/src/modelCatalog.ts; update this test if it moved")
	}
	entry := regexp.MustCompile(`(?s)(\w+):\s*\[(.*?)\]`)
	str := regexp.MustCompile(`"([^"]+)"`)
	web := map[string][]string{}
	for _, m := range entry.FindAllSubmatch(block[1], -1) {
		for _, s := range str.FindAllSubmatch(m[2], -1) {
			web[string(m[1])] = append(web[string(m[1])], string(s[1]))
		}
	}
	for provider, models := range DefaultModels {
		if strings.Join(web[provider], ",") != strings.Join(models, ",") {
			t.Errorf("%s: web has %v, catalog has %v", provider, web[provider], models)
		}
	}
	for provider := range web {
		if _, ok := DefaultModels[provider]; !ok {
			t.Errorf("web lists provider %q, which the catalog does not", provider)
		}
	}
}
