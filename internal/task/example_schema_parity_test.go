package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"go.yaml.in/yaml/v3"
)

// TestMaintainedExamplesMatchGeneratedSchemaRequiredKeys ensures checked-in
// examples satisfy the generated schema required-key contract so authoring
// samples stay aligned with schema generation.
func TestMaintainedExamplesMatchGeneratedSchemaRequiredKeys(t *testing.T) {
	t.Parallel()
	root := repoRootFromTestFile(t)

	taskSchema, err := TaskJSONSchema()
	if err != nil {
		t.Fatalf("TaskJSONSchema: %v", err)
	}
	var taskSchemaDoc map[string]any
	if err := json.Unmarshal(taskSchema, &taskSchemaDoc); err != nil {
		t.Fatalf("parse task schema: %v", err)
	}

	for _, rel := range []string{"examples/afk-task.yaml", "examples/afk-task-codex.yaml"} {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parse example YAML: %v", err)
			}
			assertRequiredSchemaKeys(t, "", doc, taskSchemaDoc)
			// Author-facing examples omit fixed defaults and lifecycle sections.
			for _, key := range []string{"mode", "status", "supervisor", "attempts", "verification", "pr"} {
				if _, ok := doc[key]; ok {
					t.Fatalf("%s must omit author-facing lifecycle/default field %q", rel, key)
				}
			}
			if wt, ok := doc["worktree"].(map[string]any); ok {
				if _, has := wt["enabled"]; has {
					t.Fatalf("%s must omit worktree.enabled", rel)
				}
			}
			if pol, ok := doc["execution_policy"].(map[string]any); ok {
				for _, key := range []string{
					"afk_decision_policy",
					"stop_on_destructive_operation",
					"stop_on_missing_secret",
					"stop_on_external_service_unavailable",
				} {
					if _, has := pol[key]; has {
						t.Fatalf("%s must omit removed execution_policy field %q", rel, key)
					}
				}
			}
		})
	}

	qualitySchema, err := profile.QualityJSONSchema()
	if err != nil {
		t.Fatalf("QualityJSONSchema: %v", err)
	}
	var qualitySchemaDoc map[string]any
	if err := json.Unmarshal(qualitySchema, &qualitySchemaDoc); err != nil {
		t.Fatalf("parse quality schema: %v", err)
	}
	qualityPath := filepath.Join(root, "examples/quality-default.yaml")
	raw, err := os.ReadFile(qualityPath)
	if err != nil {
		t.Fatal(err)
	}
	var qualityDoc map[string]any
	if err := yaml.Unmarshal(raw, &qualityDoc); err != nil {
		t.Fatalf("parse quality example: %v", err)
	}
	assertRequiredSchemaKeys(t, "", qualityDoc, qualitySchemaDoc)

	quality, err := profile.LoadQuality(qualityPath)
	if err != nil {
		t.Fatal(err)
	}
	if result := profile.ValidateQuality(quality); !result.Valid() {
		t.Fatalf("quality-default validation failed: %#v", result.Errors)
	}
	wantSeverities := []string{"critical", "high", "medium"}
	if !reflect.DeepEqual(quality.PassPolicy.BlockingSeverities, wantSeverities) {
		t.Fatalf("quality-default blocking_severities got %#v, want %#v (schema default)", quality.PassPolicy.BlockingSeverities, wantSeverities)
	}
	if strings.Contains(string(raw), "unresolved_high_findings_allowed") {
		t.Fatal("quality-default must not retain removed unresolved_high_findings_allowed")
	}
}

func assertRequiredSchemaKeys(t *testing.T, path string, doc map[string]any, schema map[string]any) {
	t.Helper()
	required, _ := schema["required"].([]any)
	props, _ := schema["properties"].(map[string]any)
	for _, rawKey := range required {
		key, ok := rawKey.(string)
		if !ok {
			continue
		}
		fieldPath := key
		if path != "" {
			fieldPath = path + "." + key
		}
		value, present := doc[key]
		if !present {
			t.Fatalf("example missing required schema key %q", fieldPath)
		}
		childSchema, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		if childSchema["type"] == "object" {
			childDoc, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("example %q must be an object for schema parity", fieldPath)
			}
			assertRequiredSchemaKeys(t, fieldPath, childDoc, childSchema)
		}
	}
}

// staleNestedDraftYAMLFields are parent/child pairs that must not appear as
// normally indented draft YAML shapes in packaged authoring guidance.
var staleNestedDraftYAMLFields = []struct {
	parent string
	child  string
}{
	{parent: "execution_policy", child: "afk_decision_policy"},
	{parent: "execution_policy", child: "stop_on_destructive_operation"},
	{parent: "execution_policy", child: "stop_on_missing_secret"},
	{parent: "execution_policy", child: "stop_on_external_service_unavailable"},
	{parent: "worktree", child: "enabled"},
}

// containsNormallyIndentedNestedYAMLKey reports whether text contains a YAML
// parent mapping with a more-indented child key line:
//
//	parent:
//	  child: ...
//
// Dotted prose mentions (worktree.enabled) and the same child key under a
// different parent (preflight.acceptance_skeleton.enabled) do not match.
func containsNormallyIndentedNestedYAMLKey(text, parent, child string) bool {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		parentIndent, parentContent, ok := splitYAMLIndent(line)
		if !ok || !isYAMLMappingKey(parentContent, parent) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			childIndent, childContent, childOK := splitYAMLIndent(lines[j])
			if !childOK {
				continue
			}
			if childIndent <= parentIndent {
				break
			}
			if isYAMLMappingKey(childContent, child) {
				return true
			}
		}
	}
	return false
}

func splitYAMLIndent(line string) (indent int, content string, ok bool) {
	if strings.TrimSpace(line) == "" {
		return 0, "", false
	}
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "#") {
		return 0, "", false
	}
	return len(line) - len(trimmed), trimmed, true
}

func isYAMLMappingKey(content, key string) bool {
	prefix := key + ":"
	if content == prefix {
		return true
	}
	return strings.HasPrefix(content, prefix+" ") || strings.HasPrefix(content, prefix+"\t")
}

func TestContainsNormallyIndentedNestedYAMLKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		text   string
		parent string
		child  string
		want   bool
	}{
		{
			name:   "rejects worktree.enabled draft shape",
			text:   "worktree:\n  enabled: true\n  branch: agent/x\n  path: ../repo.worktrees/x\n",
			parent: "worktree",
			child:  "enabled",
			want:   true,
		},
		{
			name:   "rejects execution_policy.afk_decision_policy",
			text:   "execution_policy:\n  loop_budget: 10\n  timeout_ms: 1000\n  afk_decision_policy: escalate\n",
			parent: "execution_policy",
			child:  "afk_decision_policy",
			want:   true,
		},
		{
			name:   "rejects execution_policy.stop_on_destructive_operation",
			text:   "execution_policy:\n  stop_on_destructive_operation: true\n  stop_on_missing_secret: true\n",
			parent: "execution_policy",
			child:  "stop_on_destructive_operation",
			want:   true,
		},
		{
			name:   "rejects execution_policy.stop_on_missing_secret",
			text:   "execution_policy:\n  loop_budget: 10\n  stop_on_missing_secret: true\n",
			parent: "execution_policy",
			child:  "stop_on_missing_secret",
			want:   true,
		},
		{
			name:   "rejects execution_policy.stop_on_external_service_unavailable",
			text:   "execution_policy:\n  stop_on_external_service_unavailable: true\n",
			parent: "execution_policy",
			child:  "stop_on_external_service_unavailable",
			want:   true,
		},
		{
			name:   "allows retained preflight.acceptance_skeleton.enabled",
			text:   "preflight:\n  acceptance_skeleton:\n    enabled: true\n",
			parent: "worktree",
			child:  "enabled",
			want:   false,
		},
		{
			name:   "allows acceptance_skeleton.enabled under its own parent",
			text:   "preflight:\n  acceptance_skeleton:\n    enabled: true\n",
			parent: "acceptance_skeleton",
			child:  "enabled",
			want:   true,
		},
		{
			name:   "ignores prose dotted path mentions",
			text:   "Omit `mode`, `status`, `worktree.enabled`, and AFK decision policy.\n",
			parent: "worktree",
			child:  "enabled",
			want:   false,
		},
		{
			name:   "ignores worktree without enabled child",
			text:   "worktree:\n  branch: agent/x\n  path: ../repo.worktrees/x\n",
			parent: "worktree",
			child:  "enabled",
			want:   false,
		},
		{
			name:   "ignores key prefix collisions",
			text:   "worktree:\n  enabled_extra: true\n",
			parent: "worktree",
			child:  "enabled",
			want:   false,
		},
		{
			name:   "stops at sibling blocks",
			text:   "worktree:\n  branch: agent/x\nother:\n  enabled: true\n",
			parent: "worktree",
			child:  "enabled",
			want:   false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := containsNormallyIndentedNestedYAMLKey(tc.text, tc.parent, tc.child)
			if got != tc.want {
				t.Fatalf("containsNormallyIndentedNestedYAMLKey(%q, %q) = %v, want %v\ntext:\n%s", tc.parent, tc.child, got, tc.want, tc.text)
			}
		})
	}
}

// TestPackagedTaskAuthoringOmitsStaleFixedRuntimeFieldsFromDraftGuidance
// searches skill-bundled authoring guidance for stale fixed/runtime fields that
// must not be instructed as new-draft authoring surface.
func TestPackagedTaskAuthoringOmitsStaleFixedRuntimeFieldsFromDraftGuidance(t *testing.T) {
	t.Parallel()
	root := repoRootFromTestFile(t)
	path := filepath.Join(root, "plugins/galley/skills/galley/references/task-authoring.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	// Draft shapes live under Step 7; Field Guidance is nested inside that step.
	commonShapes := sectionBetween(text, "## Step 7: Fill Skeleton With Schema", "## Step 8: Validate And Repair")
	if commonShapes == "" {
		t.Fatal("could not locate Step 7 common shapes guidance")
	}
	fieldGuidance := sectionBetween(text, "## Field Guidance", "## Step 8: Validate And Repair")
	if fieldGuidance == "" {
		fieldGuidance = sectionBetween(commonShapes, "## Field Guidance", "")
	}
	if fieldGuidance == "" {
		t.Fatal("could not locate Field Guidance section")
	}

	for _, pair := range staleNestedDraftYAMLFields {
		if containsNormallyIndentedNestedYAMLKey(commonShapes, pair.parent, pair.child) {
			t.Fatalf("task-authoring draft guidance still authors nested %s.%s YAML", pair.parent, pair.child)
		}
	}
	// Retained authoring surface must stay present; the nested guard targets
	// parent-scoped keys, so acceptance_skeleton.enabled is not treated as stale.
	if !strings.Contains(fieldGuidance, "preflight.acceptance_skeleton.enabled") {
		t.Fatal("task-authoring Field Guidance must retain preflight.acceptance_skeleton.enabled")
	}

	// Other stale top-level draft shapes / fixed defaults (not nested pairs).
	staleDraftYAML := []string{
		"\nverification:\n  commands:",
		"\nattempts:",
		"\npr:\n",
		"\nmode: \"afk\"",
		"\nmode: afk",
		"\nstatus: \"draft\"",
		"\nstatus: draft",
		"\nsupervisor:\n",
	}
	for _, needle := range staleDraftYAML {
		if strings.Contains(commonShapes, needle) {
			t.Fatalf("task-authoring common shapes/field guidance still authors stale draft YAML %q", strings.TrimSpace(needle))
		}
	}

	staleFieldBullets := []string{
		"- `mode`:",
		"- `status`:",
		"- `supervisor.review_iterations`:",
		"- `worktree.enabled`:",
		"- `execution_policy.afk_decision_policy`:",
		"- `execution_policy.stop_on_destructive_operation`:",
		"- `execution_policy.stop_on_missing_secret`:",
		"- `execution_policy.stop_on_external_service_unavailable`:",
		"- `attempts`:",
		"- `verification`:",
		"- `pr`:",
	}
	for _, needle := range staleFieldBullets {
		if strings.Contains(fieldGuidance, needle) {
			t.Fatalf("task-authoring Field Guidance still instructs authoring stale field %q", needle)
		}
	}

	for _, want := range []string{
		"Omit `mode`, `status`, `worktree.enabled`",
		"daemon-owned lifecycle sections",
		"Do not author `worktree.enabled`",
	} {
		if !strings.Contains(fieldGuidance, want) {
			t.Fatalf("task-authoring Field Guidance missing omit guidance %q", want)
		}
	}
}

func sectionBetween(text, start, end string) string {
	i := strings.Index(text, start)
	if i < 0 {
		return ""
	}
	rest := text[i:]
	if end == "" {
		return rest
	}
	j := strings.Index(rest[len(start):], end)
	if j < 0 {
		return rest
	}
	return rest[:len(start)+j]
}
