package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var updateMergeDiff = flag.Bool("update", false, "rewrite testdata/merge_effective_config_diff.txt")

const mergeDiffGolden = "testdata/merge_effective_config_diff.txt"

// legacyStructMerge reproduces the pre-fix overlay merge, which decoded each
// overlay into YAMLConfig and re-marshaled it, so every non-omitempty field the
// overlay omitted overrode base with its zero value. It exists only so
// TestRealEnvConfigs_MergeEffectiveDiff can show what the raw-YAML merge changed.
func legacyStructMerge(t *testing.T, base *YAMLConfig, overlayPath string) *YAMLConfig {
	t.Helper()
	data, err := os.ReadFile(overlayPath)
	require.NoError(t, err)
	var overlay YAMLConfig
	require.NoError(t, yaml.Unmarshal(data, &overlay))
	merged, err := mergeConfigMap(base, toMap(t, &overlay))
	require.NoError(t, err)
	return merged
}

func toMap(t *testing.T, cfg *YAMLConfig) map[string]any {
	t.Helper()
	b, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(b, &m))
	return m
}

func flattenConfig(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flattenConfig(p, child, out)
		}
	default:
		b, _ := yaml.Marshal(x)
		var node yaml.Node
		_ = yaml.Unmarshal(b, &node)
		if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
			node.Content[0].Style = yaml.FlowStyle
			b, _ = yaml.Marshal(node.Content[0])
		}
		out[prefix] = strings.TrimSpace(string(b))
	}
}

func diffConfigs(t *testing.T, before, after *YAMLConfig) []string {
	t.Helper()
	b := map[string]string{}
	a := map[string]string{}
	flattenConfig("", toMap(t, before), b)
	flattenConfig("", toMap(t, after), a)
	keys := map[string]struct{}{}
	for k := range b {
		keys[k] = struct{}{}
	}
	for k := range a {
		keys[k] = struct{}{}
	}
	var lines []string
	for k := range keys {
		bv, bok := b[k]
		av, aok := a[k]
		if bok && aok && bv == av {
			continue
		}
		if !bok {
			bv = "<absent>"
		}
		if !aok {
			av = "<absent>"
		}
		lines = append(lines, fmt.Sprintf("%s: %s -> %s", k, bv, av))
	}
	sort.Strings(lines)
	return lines
}

func TestRealEnvConfigs_StagingRejectsBYOKeys(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	t.Setenv("ENVIRONMENT", "staging")
	cfg, err := LoadAndMergeConfigs([]string{
		filepath.Join(configsDir, "base.yml"),
		filepath.Join(configsDir, "staging.yml"),
	})
	require.NoError(t, err)
	require.False(t, cfg.Features.BYOKeys.Enabled)
}

func TestRealEnvConfigs_EditorDailyCapFromBase(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	for _, env := range []string{"staging", "production"} {
		t.Setenv("ENVIRONMENT", env)
		cfg, err := LoadAndMergeConfigs([]string{
			filepath.Join(configsDir, "base.yml"),
			filepath.Join(configsDir, env+".yml"),
		})
		require.NoError(t, err, env)
		require.Equal(t, int64(10000), cfg.Features.AdminDashboard.EditorLimits.MaxDailyCostLimitCents, env)
	}
}

// TestRealEnvConfigs_MergeEffectiveDiff records, per environment, every
// effective setting that differs between the legacy struct round-trip merge
// and the raw-YAML merge. Changing a config file so that this list changes
// fails the test until the golden file is regenerated with -update and the
// change is reviewed.
func TestRealEnvConfigs_MergeEffectiveDiff(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	basePath := filepath.Join(configsDir, "base.yml")

	cases := []struct {
		name    string
		env     string
		overlay []string
	}{
		{"dev", "dev", []string{"dev.yml"}},
		{"staging", "staging", []string{"staging.yml"}},
		{"staging+sidecar", "staging", []string{"staging.yml", "sidecar.yml"}},
		{"production", "production", []string{"production.yml"}},
		{"production+sidecar", "production", []string{"production.yml", "sidecar.yml"}},
		{"fuzz", "fuzz", []string{"fuzz.yml"}},
		{"fuzz-mem", "fuzz-mem", []string{"fuzz-mem.yml"}},
	}

	var report strings.Builder
	for _, tc := range cases {
		t.Setenv("ENVIRONMENT", tc.env)

		paths := []string{basePath}
		for _, o := range tc.overlay {
			paths = append(paths, filepath.Join(configsDir, o))
		}
		after, err := LoadAndMergeConfigs(paths)
		require.NoError(t, err, tc.name)

		before, err := LoadYAMLConfig(basePath)
		require.NoError(t, err)
		// The env overlay used the struct round-trip; the sidecar profile
		// was already merged as raw YAML.
		before = legacyStructMerge(t, before, paths[1])
		for _, p := range paths[2:] {
			before, err = mergeConfigFromYAMLFile(before, p)
			require.NoError(t, err)
		}

		fmt.Fprintf(&report, "## %s\n", tc.name)
		for _, line := range diffConfigs(t, before, after) {
			report.WriteString(line + "\n")
		}
		report.WriteString("\n")
	}

	if *updateMergeDiff {
		require.NoError(t, os.MkdirAll(filepath.Dir(mergeDiffGolden), 0o755))
		require.NoError(t, os.WriteFile(mergeDiffGolden, []byte(report.String()), 0o644))
		return
	}
	want, err := os.ReadFile(mergeDiffGolden)
	require.NoError(t, err, "run go test ./internal/config -run MergeEffectiveDiff -update")
	require.Equal(t, string(want), report.String(),
		"effective config diff changed; review it, then regenerate with -update")
}
