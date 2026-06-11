package redact

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// recognizersYAMLPath returns the absolute path to the repo-root
// “recognizers.yaml“ file regardless of where “go test“ was invoked
// from. Important because “go test ./internal/redact/...“ and
// “go test ./...“ run with different working directories.
func recognizersYAMLPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed; cannot locate test file path")
	}
	// thisFile = .../llm-proxy/internal/redact/recognizers_yaml_test.go
	// repo root = three directories up.
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(repoRoot, "recognizers.yaml")
}

// recognizerEntry mirrors the subset of the recognizers.yaml schema
// that we care about. Anything we don't list here is simply ignored
// by the YAML decoder.
type recognizerEntry struct {
	Name            string `yaml:"name"`
	SupportedLang   string `yaml:"supported_language"`
	SupportedEntity string `yaml:"supported_entity"`
	Type            string `yaml:"type"`
}

type recognizersFile struct {
	SupportedLanguages []string          `yaml:"supported_languages"`
	GlobalRegexFlags   int               `yaml:"global_regex_flags"`
	Recognizers        []recognizerEntry `yaml:"recognizers"`
}

func loadRecognizersYAML(t *testing.T) recognizersFile {
	t.Helper()
	path := recognizersYAMLPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f recognizersFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse %s as YAML: %v", path, err)
	}
	return f
}

// TestRecognizersYAML_ParsesAndHasMandatoryShape asserts the file the
// docker-compose mount references actually exists, parses cleanly, and
// has the top-level keys Presidio's loader requires. A regression here
// (e.g. someone re-nesting under “recognizer_registry“) shows up as
// "Invalid recognizer registry configuration" inside the sidecar
// container — by the time you see that, your --pii integration tests
// have already failed with cryptic timeouts. This static test fails
// at unit-test speed instead.
func TestRecognizersYAML_ParsesAndHasMandatoryShape(t *testing.T) {
	f := loadRecognizersYAML(t)

	if len(f.SupportedLanguages) == 0 {
		t.Errorf("supported_languages is empty; Presidio rejects file without it")
	}
	foundEN := false
	for _, l := range f.SupportedLanguages {
		if l == "en" {
			foundEN = true
		}
	}
	if !foundEN {
		t.Errorf("supported_languages does not include \"en\"; got %v", f.SupportedLanguages)
	}
	if len(f.Recognizers) == 0 {
		t.Fatalf("recognizers list is empty; YAML mount would no-op silently")
	}
}

// TestRecognizersYAML_MatchesDefaultEntityTypes is the linchpin: the
// set of supported_entity values in recognizers.yaml MUST equal
// DefaultEntityTypes. If they drift, two failure modes emerge:
//
//   - YAML lists an entity that DefaultEntityTypes doesn't ➜ the
//     wire-side scope filter drops it before /analyze ever sees it.
//     The recognizer is loaded but never asked to score anything, so
//     it's dead code.
//
//   - DefaultEntityTypes lists an entity that YAML doesn't ➜ the
//     scope filter passes it on the wire but Presidio has no
//     recognizer to score it. The /analyze response returns no spans
//     and the proxy advertises a redaction guarantee it cannot
//     deliver.
//
// Either way the user-visible behaviour is "the entity I expect to be
// redacted, isn't" — surfaced as a production incident rather than a
// failing test. So we make it a failing test.
func TestRecognizersYAML_MatchesDefaultEntityTypes(t *testing.T) {
	f := loadRecognizersYAML(t)

	yamlEntities := make(map[string]struct{}, len(f.Recognizers))
	for _, r := range f.Recognizers {
		if r.SupportedEntity == "" {
			t.Errorf("recognizer %q has no supported_entity; Presidio will refuse to load it",
				r.Name)
			continue
		}
		yamlEntities[r.SupportedEntity] = struct{}{}
	}

	// LOCATION rides on SpacyRecognizer (already present as PERSON);
	// Presidio's spaCy adapter emits both PERSON and LOCATION from a
	// single registry entry. The YAML lists the recognizer once, but
	// DefaultEntityTypes reasonably names both entities. Special-case
	// LOCATION here so the equality check passes.
	yamlEntities["LOCATION"] = struct{}{}

	codeEntities := make(map[string]struct{}, len(DefaultEntityTypes))
	for _, e := range DefaultEntityTypes {
		codeEntities[e] = struct{}{}
	}

	var onlyInYAML, onlyInCode []string
	for e := range yamlEntities {
		if _, ok := codeEntities[e]; !ok {
			onlyInYAML = append(onlyInYAML, e)
		}
	}
	for e := range codeEntities {
		if _, ok := yamlEntities[e]; !ok {
			onlyInCode = append(onlyInCode, e)
		}
	}
	sort.Strings(onlyInYAML)
	sort.Strings(onlyInCode)

	if len(onlyInYAML) > 0 {
		t.Errorf("recognizers.yaml lists supported_entity values not in "+
			"DefaultEntityTypes (dead-code recognizers): %v", onlyInYAML)
	}
	if len(onlyInCode) > 0 {
		t.Errorf("DefaultEntityTypes references entities with no recognizer "+
			"in recognizers.yaml (proxy advertises redaction it cannot deliver): %v",
			onlyInCode)
	}
}

// TestRecognizersYAML_CustomRecognizersHavePatterns guards the
// Instawork custom recognizers specifically: a “type: custom“ entry
// without a “patterns“ block silently never fires (the loader
// accepts it, the recognizer just has nothing to match). Easy to do
// when copy-pasting from a predefined entry, hard to spot.
func TestRecognizersYAML_CustomRecognizersHavePatterns(t *testing.T) {
	// Re-parse with a richer schema that carries the patterns list.
	type custom struct {
		Name            string `yaml:"name"`
		Type            string `yaml:"type"`
		SupportedEntity string `yaml:"supported_entity"`
		Patterns        []struct {
			Name  string  `yaml:"name"`
			Regex string  `yaml:"regex"`
			Score float64 `yaml:"score"`
		} `yaml:"patterns"`
		Context []string `yaml:"context"`
	}
	type file struct {
		Recognizers []custom `yaml:"recognizers"`
	}
	raw, err := os.ReadFile(recognizersYAMLPath(t))
	if err != nil {
		t.Fatalf("read recognizers.yaml: %v", err)
	}
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, r := range f.Recognizers {
		if r.Type != "custom" {
			continue
		}
		if !strings.HasPrefix(r.Name, "Instawork") {
			t.Errorf("custom recognizer %q does not have the 'Instawork' name "+
				"prefix; please name custom recognizers consistently to keep "+
				"the deployed config diff readable", r.Name)
		}
		if len(r.Patterns) == 0 {
			t.Errorf("custom recognizer %q has no patterns; loader accepts "+
				"the entry but the recognizer cannot match anything", r.Name)
			continue
		}
		for _, p := range r.Patterns {
			if p.Regex == "" {
				t.Errorf("custom recognizer %q pattern %q has empty regex",
					r.Name, p.Name)
			}
			if p.Score < 0.01 || p.Score > 1.0 {
				t.Errorf("custom recognizer %q pattern %q score %v out of "+
					"sane (0.01, 1.0] range", r.Name, p.Name, p.Score)
			}
		}
	}
}
