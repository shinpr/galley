package profile

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/provider"
	"go.yaml.in/yaml/v3"
)

// Quality describes review requirements that should shape executor verification.
type Quality struct {
	ID                   string               `yaml:"id" json:"id"`
	RequiredChecks       []RequiredCheck      `yaml:"required_checks" json:"required_checks"`
	ReviewDimensions     []ReviewDimension    `yaml:"review_dimensions" json:"review_dimensions"`
	EvidenceRequirements EvidenceRequirements `yaml:"evidence_requirements" json:"evidence_requirements"`
	PassPolicy           PassPolicy           `yaml:"pass_policy" json:"pass_policy"`
}

type RequiredCheck struct {
	ID                string   `yaml:"id" json:"id"`
	PreferredCommands []string `yaml:"preferred_commands" json:"preferred_commands"`
	Required          bool     `yaml:"required" json:"required"`
}

type ReviewDimension struct {
	ID       string `yaml:"id" json:"id"`
	Weight   int    `yaml:"weight" json:"weight"`
	Required bool   `yaml:"required" json:"required"`
	Pass     string `yaml:"pass" json:"pass"`
}

type EvidenceRequirements struct {
	FileLineReferences bool `yaml:"file_line_references" json:"file_line_references"`
	CommandOutputs     bool `yaml:"command_outputs" json:"command_outputs"`
}

type PassPolicy struct {
	RequiredDimensionsMustPass bool     `yaml:"required_dimensions_must_pass" json:"required_dimensions_must_pass"`
	MinScore                   int      `yaml:"min_score" json:"min_score"`
	UnresolvedHighAllowed      int      `yaml:"unresolved_high_findings_allowed" json:"unresolved_high_findings_allowed"`
	BlockingSeverities         []string `yaml:"blocking_severities" json:"blocking_severities"`
}

// Environment describes local execution capabilities and constraints.
type Environment struct {
	ID             string                   `yaml:"id" json:"id"`
	CWD            string                   `yaml:"cwd" json:"cwd"`
	Commands       map[string]string        `yaml:"commands" json:"commands"`
	Executor       *ExecutorDefault         `yaml:"executor,omitempty" json:"executor,omitempty"`
	Supervisor     *SupervisorDefault       `yaml:"supervisor,omitempty" json:"supervisor,omitempty"`
	RequiredChecks RequiredCheckEnvironment `yaml:"required_checks,omitempty" json:"required_checks,omitempty"`
	Constraints    Constraints              `yaml:"constraints" json:"constraints"`
	PR             PRSettings               `yaml:"pr,omitempty" json:"pr,omitempty"`
	Worktree       WorktreeSettings         `yaml:"worktree,omitempty" json:"worktree,omitempty"`
	// Setup describes the prior plan the setup executor should try when
	// preparing a fresh task worktree before the implementation executor begins.
	// When absent, the setup executor may discover the successful setup plan and
	// write it back to environment.yaml so subsequent tasks reuse the learned
	// setup without rediscovery.
	Setup *SetupPlan `yaml:"setup,omitempty" json:"setup,omitempty"`
}

// SetupPlan is the ordered list of commands the setup executor should use as a
// prior setup plan. The plan may be authored by the operator or learned by the
// setup executor and persisted back to environment.yaml.
type SetupPlan struct {
	Commands []SetupCommand `yaml:"commands" json:"commands"`
}

// SetupCommand is a single command in a SetupPlan. The optional Why string
// explains the command's purpose so the persisted environment.yaml is readable
// by humans after a learned setup is written back.
type SetupCommand struct {
	Run string `yaml:"run" json:"run"`
	Why string `yaml:"why,omitempty" json:"why,omitempty"`
}

const (
	MaxSetupCommandRunLength = 4096
	MaxSetupCommandWhyLength = 1024
)

type ExecutorDefault struct {
	DefaultCLI string `yaml:"default_cli,omitempty" json:"default_cli,omitempty"`
}

// SupervisorDefault selects the repository-scoped review supervisor for tasks
// whose `scope.cwd` resolves to this environment profile. When `default_cli`
// is set, the daemon uses it for that task even when daemon CLI startup
// options, `daemon.yaml`, or the built-in default would otherwise pick a
// different supervisor. Allowed values are `claude`, `codex`, and `glm`.
type SupervisorDefault struct {
	DefaultCLI string `yaml:"default_cli,omitempty" json:"default_cli,omitempty"`
	// Model is passed unchanged to the provider CLI. Empty preserves its default.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// Effort is validated against the effective provider before review; empty keeps its CLI default.
	Effort string `yaml:"effort,omitempty" json:"effort,omitempty"`
}

type RequiredCheckEnvironment struct {
	Shell     string `yaml:"shell,omitempty" json:"shell,omitempty"`
	ShellPath string `yaml:"shell_path,omitempty" json:"shell_path,omitempty"`
}

type Constraints struct {
	Network             string `yaml:"network" json:"network"`
	SecretsPolicy       string `yaml:"secrets_policy" json:"secrets_policy"`
	DestructiveCommands string `yaml:"destructive_commands" json:"destructive_commands"`
}

type PRSettings struct {
	Enabled  bool              `yaml:"enabled" json:"enabled"`
	Base     string            `yaml:"base,omitempty" json:"base,omitempty"`
	Comments PRCommentSettings `yaml:"comments,omitempty" json:"comments,omitempty"`
}

type PRCommentSettings struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	Reply   bool `yaml:"reply" json:"reply"`
}

type WorktreeSettings struct {
	Cleanup *bool `yaml:"cleanup,omitempty" json:"cleanup,omitempty"`
}

// Bundle is the optional profile context included in work orders.
type Bundle struct {
	Quality     *Quality     `json:"quality,omitempty"`
	Environment *Environment `json:"environment,omitempty"`
}

// ValidationResult reports profile validation diagnostics.
type ValidationResult struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

func (r ValidationResult) Valid() bool {
	return len(r.Errors) == 0
}

func LoadQuality(path string) (Quality, error) {
	var q Quality
	if err := loadYAML(path, &q); err != nil {
		return Quality{}, err
	}
	return q, nil
}

func LoadEnvironment(path string) (Environment, error) {
	var env Environment
	if err := loadYAML(path, &env); err != nil {
		return Environment{}, err
	}
	return env, nil
}

func LoadBundle(qualityPath, environmentPath string) (Bundle, error) {
	var bundle Bundle
	if qualityPath != "" {
		quality, err := LoadQuality(qualityPath)
		if err != nil {
			return Bundle{}, err
		}
		if result := ValidateQuality(quality); !result.Valid() {
			return Bundle{}, fmt.Errorf("invalid quality profile %s: %s", qualityPath, strings.Join(result.Errors, "; "))
		}
		bundle.Quality = &quality
	}
	if environmentPath != "" {
		env, err := LoadEnvironment(environmentPath)
		if err != nil {
			return Bundle{}, err
		}
		if result := ValidateEnvironment(env); !result.Valid() {
			return Bundle{}, fmt.Errorf("invalid environment profile %s: %s", environmentPath, strings.Join(result.Errors, "; "))
		}
		bundle.Environment = &env
	}
	return bundle, nil
}

func ValidateQuality(q Quality) ValidationResult {
	result := ValidationResult{Kind: "quality", ID: q.ID}
	require(&result, q.ID != "", "id is required")
	require(&result, q.PassPolicy.MinScore >= 0 && q.PassPolicy.MinScore <= 100, "pass_policy.min_score must be 0..100")
	for i, severity := range q.PassPolicy.BlockingSeverities {
		require(&result, validFindingSeverity(severity), "pass_policy.blocking_severities[%d] is invalid", i)
	}
	for i, check := range q.RequiredChecks {
		prefix := fmt.Sprintf("required_checks[%d]", i)
		require(&result, check.ID != "", "%s.id is required", prefix)
		if check.Required && len(check.PreferredCommands) == 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("%s.preferred_commands is required when check is required", prefix))
		}
	}
	dimensionIDs := make(map[string]bool, len(q.ReviewDimensions))
	for i, dim := range q.ReviewDimensions {
		prefix := fmt.Sprintf("review_dimensions[%d]", i)
		require(&result, dim.ID != "", "%s.id is required", prefix)
		require(&result, !dimensionIDs[dim.ID], "%s.id %q is duplicated", prefix, dim.ID)
		dimensionIDs[dim.ID] = true
		require(&result, dim.Weight >= 0, "%s.weight cannot be negative", prefix)
		require(&result, dim.Pass != "", "%s.pass is required", prefix)
	}
	return result
}

func validFindingSeverity(value string) bool {
	switch value {
	case "critical", "high", "medium", "low":
		return true
	default:
		return false
	}
}

func ValidateEnvironment(env Environment) ValidationResult {
	result := ValidationResult{Kind: "environment", ID: env.ID}
	require(&result, env.ID != "", "id is required")
	require(&result, env.CWD != "", "cwd is required")
	if len(env.Commands) == 0 {
		result.Warnings = append(result.Warnings, "commands is empty")
	}
	require(&result, env.Constraints.Network != "", "constraints.network is required")
	require(&result, env.Constraints.SecretsPolicy != "", "constraints.secrets_policy is required")
	require(&result, env.Constraints.DestructiveCommands != "", "constraints.destructive_commands is required")
	if env.Executor != nil && env.Executor.DefaultCLI != "" {
		require(&result, validExecutorCLI(env.Executor.DefaultCLI), "executor.default_cli must be one of: %s", strings.Join(provider.ExecutorIDs(), ", "))
	}
	if env.Supervisor != nil && env.Supervisor.DefaultCLI != "" {
		require(&result, daemonconfig.IsValidSupervisor(env.Supervisor.DefaultCLI), "supervisor.default_cli must be one of: %s", strings.Join(daemonconfig.SupervisorCLIs(), ", "))
	}
	if env.Supervisor != nil && env.Supervisor.Effort != "" {
		// Without default_cli, profile validation can enforce only the provider union; preflight narrows it later.
		if env.Supervisor.DefaultCLI != "" {
			if efforts, ok := provider.EffortsForID(env.Supervisor.DefaultCLI); ok {
				require(&result, slices.Contains(efforts, env.Supervisor.Effort), "supervisor.effort for %s must be one of: %s", env.Supervisor.DefaultCLI, strings.Join(efforts, ", "))
			}
		} else {
			require(&result, slices.Contains(provider.SupervisorEfforts(), env.Supervisor.Effort), "supervisor.effort must be one of: %s", strings.Join(provider.SupervisorEfforts(), ", "))
		}
	}
	if env.RequiredChecks.Shell != "" {
		require(&result, validRequiredCheckShell(env.RequiredChecks.Shell), "required_checks.shell must be one of: auto, sh, bash, cmd, powershell, pwsh")
	}
	if env.RequiredChecks.ShellPath != "" {
		if strings.TrimSpace(env.RequiredChecks.ShellPath) != env.RequiredChecks.ShellPath {
			result.Errors = append(result.Errors, "required_checks.shell_path must not have leading or trailing whitespace")
		}
		// required_checks.shell_path is the more specific executable selection.
		// When the executable basename is one of the recognized shells, Galley
		// can infer the invocation style and shell_path may stand alone. When
		// the basename is not recognized, an explicit non-auto
		// required_checks.shell is required as fallback kind metadata.
		if InferRequiredCheckShellKind(env.RequiredChecks.ShellPath) == "" {
			switch env.RequiredChecks.Shell {
			case "", "auto":
				result.Errors = append(result.Errors, "required_checks.shell_path basename is not a recognized shell executable; set an explicit required_checks.shell kind (sh, bash, cmd, powershell, or pwsh) as fallback metadata")
			}
		}
	}
	if env.PR.Comments.Reply && !env.PR.Comments.Enabled {
		result.Warnings = append(result.Warnings, "pr.comments.reply is set while pr.comments.enabled is false")
	}
	if env.Setup != nil {
		if len(env.Setup.Commands) == 0 {
			result.Errors = append(result.Errors, "setup.commands must not be empty when setup is present")
		}
		for i, cmd := range env.Setup.Commands {
			prefix := fmt.Sprintf("setup.commands[%d]", i)
			if strings.TrimSpace(cmd.Run) == "" {
				result.Errors = append(result.Errors, fmt.Sprintf("%s.run is required", prefix))
			}
			validateSetupCommandText(&result, prefix+".run", cmd.Run, MaxSetupCommandRunLength)
			if cmd.Why != "" {
				validateSetupCommandText(&result, prefix+".why", cmd.Why, MaxSetupCommandWhyLength)
			}
		}
	}
	return result
}

func validateSetupCommandText(result *ValidationResult, field, value string, max int) {
	if len(value) > max {
		result.Errors = append(result.Errors, fmt.Sprintf("%s must be at most %d bytes", field, max))
	}
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			result.Errors = append(result.Errors, fmt.Sprintf("%s must not contain control characters other than tab or newline", field))
			return
		}
	}
}

func validExecutorCLI(value string) bool {
	return provider.IsExecutor(value)
}

func validRequiredCheckShell(value string) bool {
	switch value {
	case "auto", "sh", "bash", "cmd", "powershell", "pwsh":
		return true
	default:
		return false
	}
}

// InferRequiredCheckShellKind returns the required-check shell kind implied by
// a shell_path executable name, or "" when the basename is not one of the
// recognized shells. The match is case-insensitive, ignores a trailing `.exe`
// suffix, and tolerates either forward-slash or backslash separators so
// Windows paths can be inferred on any host. The caller is responsible for
// pairing the inferred kind with the original path; only the basename is
// considered.
func InferRequiredCheckShellKind(shellPath string) string {
	name := shellPath
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "bash", "sh", "cmd", "powershell", "pwsh":
		return name
	}
	return ""
}

// UpdateEnvironmentSetup atomically rewrites the setup field of the environment
// YAML at path, preserving unrelated profile content. The rewritten document is
// decoded and validated in memory BEFORE the atomic rename — an invalid setup
// never reaches the published file path, and concurrent readers therefore never
// observe a transiently invalid state. The function returns the prior setup
// plan (nil when absent) so callers can persist a before/after record of the
// change.
func UpdateEnvironmentSetup(path string, plan SetupPlan) (*SetupPlan, error) {
	if path == "" {
		return nil, fmt.Errorf("environment profile path is required")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read environment profile %s: %w", path, err)
	}
	// Decode into a generic yaml.Node so unrelated fields, ordering, and
	// comments survive the rewrite. Replacing the setup mapping in-place keeps
	// the rest of the document untouched.
	var root yaml.Node
	if err := yaml.Unmarshal(original, &root); err != nil {
		return nil, fmt.Errorf("decode environment profile %s: %w", path, err)
	}
	prior := extractEnvironmentSetup(&root)
	if err := replaceEnvironmentSetup(&root, plan); err != nil {
		return nil, fmt.Errorf("update environment profile setup: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("encode environment profile %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("flush environment profile %s: %w", path, err)
	}
	// Validate the rewritten bytes in memory before publication. This is the
	// publication boundary: parse the new document with the same KnownFields
	// decoder used by LoadEnvironment, then run ValidateEnvironment on the
	// decoded value. If either step fails the rewrite is rejected without
	// touching the on-disk file at path, so the original file remains the
	// canonical state for any reader.
	var updated Environment
	decoder := yaml.NewDecoder(bytes.NewReader(buf.Bytes()))
	decoder.KnownFields(true)
	if err := decoder.Decode(&updated); err != nil {
		return nil, fmt.Errorf("decode rewritten environment profile: %w", err)
	}
	if vr := ValidateEnvironment(updated); !vr.Valid() {
		return nil, fmt.Errorf("environment profile rewrite failed validation: %s", strings.Join(vr.Errors, "; "))
	}
	tmp, err := os.CreateTemp(filepathDir(path), ".environment-setup-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("stage environment profile rewrite: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write environment profile rewrite: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close environment profile rewrite: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, fmt.Errorf("rename environment profile rewrite: %w", err)
	}
	cleanup = false
	return prior, nil
}

func extractEnvironmentSetup(root *yaml.Node) *SetupPlan {
	mapping := documentMapping(root)
	if mapping == nil {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "setup" {
			var plan SetupPlan
			if err := mapping.Content[i+1].Decode(&plan); err != nil {
				return nil
			}
			return &plan
		}
	}
	return nil
}

func replaceEnvironmentSetup(root *yaml.Node, plan SetupPlan) error {
	mapping := documentMapping(root)
	if mapping == nil {
		// Build a fresh mapping when the document was empty.
		mapping = &yaml.Node{Kind: yaml.MappingNode}
		root.Kind = yaml.DocumentNode
		root.Content = []*yaml.Node{mapping}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "setup"}
	valueNode := &yaml.Node{}
	if err := valueNode.Encode(plan); err != nil {
		return err
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "setup" {
			mapping.Content[i+1] = valueNode
			return nil
		}
	}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return nil
}

func documentMapping(root *yaml.Node) *yaml.Node {
	if root == nil {
		return nil
	}
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

func loadYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func require(result *ValidationResult, ok bool, format string, args ...any) {
	if ok {
		return
	}
	result.Errors = append(result.Errors, fmt.Sprintf(format, args...))
}
