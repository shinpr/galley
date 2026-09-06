package profile

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/shinpr/galley/internal/fileutil"
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

// ExecutorDefault provides repository defaults for omitted task executor fields.
type ExecutorDefault struct {
	DefaultCLI string `yaml:"default_cli,omitempty" json:"default_cli,omitempty"`
	Model      string `yaml:"model,omitempty" json:"model,omitempty"`
	Effort     string `yaml:"effort,omitempty" json:"effort,omitempty"`
}

// SupervisorDefault selects the repository-scoped review supervisor for tasks
// whose `scope.cwd` resolves to this environment profile. When `default_cli`
// is set, the daemon uses it for that task even when daemon CLI startup
// options, `daemon.yaml`, or the built-in default would otherwise pick a
// different supervisor. Allowed values come from the provider registry.
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
	if err := loadYAML(path, &q, qualitySchemaDocument()); err != nil {
		return Quality{}, err
	}
	return q, nil
}

func LoadEnvironment(path string) (Environment, error) {
	var env Environment
	if err := loadYAML(path, &env, environmentSchemaDocument()); err != nil {
		return Environment{}, err
	}
	return env, nil
}

func LoadBundle(qualityPath, environmentPath string) (Bundle, error) {
	var bundle Bundle
	quality, err := loadValidQuality(qualityPath)
	if err != nil {
		return Bundle{}, err
	}
	env, err := loadValidEnvironment(environmentPath)
	if err != nil {
		return Bundle{}, err
	}
	bundle.Quality, bundle.Environment = quality, env
	return bundle, nil
}

func loadValidQuality(path string) (*Quality, error) {
	if path == "" {
		return nil, nil
	}
	quality, err := LoadQuality(path)
	if err != nil {
		return nil, err
	}
	if result := ValidateQuality(quality); !result.Valid() {
		return nil, fmt.Errorf("invalid quality profile %s: %s", path, strings.Join(result.Errors, "; "))
	}
	return &quality, nil
}

func loadValidEnvironment(path string) (*Environment, error) {
	if path == "" {
		return nil, nil
	}
	env, err := LoadEnvironment(path)
	if err != nil {
		return nil, err
	}
	if result := ValidateEnvironment(env); !result.Valid() {
		return nil, fmt.Errorf("invalid environment profile %s: %s", path, strings.Join(result.Errors, "; "))
	}
	return &env, nil
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
	validateExecutorDefault(&result, env.Executor)
	validateSupervisorDefault(&result, env.Supervisor)
	validateRequiredChecks(&result, env.RequiredChecks)
	if env.PR.Comments.Reply && !env.PR.Comments.Enabled {
		result.Warnings = append(result.Warnings, "pr.comments.reply is set while pr.comments.enabled is false")
	}
	validateSetupPlan(&result, env.Setup)
	return result
}

func validateExecutorDefault(result *ValidationResult, executor *ExecutorDefault) {
	if executor == nil {
		return
	}
	if executor.DefaultCLI != "" {
		require(result, validExecutorCLI(executor.DefaultCLI), "executor.default_cli must be one of: %s", strings.Join(provider.ExecutorIDs(), ", "))
	}
	if executor.Effort == "" {
		return
	}
	if executor.DefaultCLI == "" {
		require(result, slices.Contains(provider.ExecutorEfforts(), executor.Effort), "executor.effort must be one of: %s", strings.Join(provider.ExecutorEfforts(), ", "))
		return
	}
	if efforts, ok := provider.EffortsForID(executor.DefaultCLI); ok {
		require(result, slices.Contains(efforts, executor.Effort), "executor.effort for %s must be one of: %s", executor.DefaultCLI, strings.Join(efforts, ", "))
	}
}

func validateSupervisorDefault(result *ValidationResult, supervisor *SupervisorDefault) {
	if supervisor == nil {
		return
	}
	if supervisor.DefaultCLI != "" {
		require(result, provider.IsSupervisor(supervisor.DefaultCLI), "supervisor.default_cli must be one of: %s", strings.Join(provider.SupervisorIDs(), ", "))
	}
	if supervisor.Effort == "" {
		return
	}
	// Without default_cli, profile validation can enforce only the provider
	// union; preflight narrows it later.
	if supervisor.DefaultCLI == "" {
		require(result, slices.Contains(provider.SupervisorEfforts(), supervisor.Effort), "supervisor.effort must be one of: %s", strings.Join(provider.SupervisorEfforts(), ", "))
		return
	}
	if efforts, ok := provider.EffortsForID(supervisor.DefaultCLI); ok {
		require(result, slices.Contains(efforts, supervisor.Effort), "supervisor.effort for %s must be one of: %s", supervisor.DefaultCLI, strings.Join(efforts, ", "))
	}
}

func validateRequiredChecks(result *ValidationResult, checks RequiredCheckEnvironment) {
	if checks.Shell != "" {
		require(result, validRequiredCheckShell(checks.Shell), "required_checks.shell must be one of: auto, sh, bash, cmd, powershell, pwsh")
	}
	if checks.ShellPath == "" {
		return
	}
	if strings.TrimSpace(checks.ShellPath) != checks.ShellPath {
		result.Errors = append(result.Errors, "required_checks.shell_path must not have leading or trailing whitespace")
	}
	// A recognized shell_path basename lets Galley infer the invocation style, so
	// shell_path stands alone; otherwise an explicit shell kind supplies it.
	if InferRequiredCheckShellKind(checks.ShellPath) != "" {
		return
	}
	switch checks.Shell {
	case "", "auto":
		result.Errors = append(result.Errors, "required_checks.shell_path basename is not a recognized shell executable; set an explicit required_checks.shell kind (sh, bash, cmd, powershell, or pwsh) as fallback metadata")
	}
}

func validateSetupPlan(result *ValidationResult, setup *SetupPlan) {
	if setup == nil {
		return
	}
	if len(setup.Commands) == 0 {
		result.Errors = append(result.Errors, "setup.commands must not be empty when setup is present")
	}
	for i, cmd := range setup.Commands {
		prefix := fmt.Sprintf("setup.commands[%d]", i)
		if strings.TrimSpace(cmd.Run) == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("%s.run is required", prefix))
		}
		validateSetupCommandText(result, prefix+".run", cmd.Run, MaxSetupCommandRunLength)
		if cmd.Why != "" {
			validateSetupCommandText(result, prefix+".why", cmd.Why, MaxSetupCommandWhyLength)
		}
	}
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
	// Validate rewritten bytes with the same required-field and known-value
	// checks as LoadEnvironment before publishing the atomic replacement.
	var updated Environment
	if err := decodeProfileYAML(buf.Bytes(), &updated, environmentSchemaDocument()); err != nil {
		return nil, fmt.Errorf("decode rewritten environment profile: %w", err)
	}
	if vr := ValidateEnvironment(updated); !vr.Valid() {
		return nil, fmt.Errorf("environment profile rewrite failed validation: %s", strings.Join(vr.Errors, "; "))
	}
	if err := fileutil.WriteFileAtomic(path, buf.Bytes(), 0o600); err != nil {
		return nil, fmt.Errorf("publish environment profile rewrite: %w", err)
	}
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
		return fmt.Errorf("encode executor plan: %w", err)
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

func loadYAML(path string, out any, schema map[string]any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := decodeProfileYAML(data, out, schema); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// decodeProfileYAML uses the schema to distinguish missing keys from valid zero values.
// ValidateQuality and ValidateEnvironment own decoded-value semantics.
func decodeProfileYAML(data []byte, out any, schema map[string]any) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse profile YAML: %w", err)
	}
	if err := root.Decode(out); err != nil {
		return fmt.Errorf("decode profile document: %w", err)
	}
	missing := missingRequiredYAMLFields(&root, schema, "")
	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func missingRequiredYAMLFields(node *yaml.Node, schema map[string]any, path string) []string {
	node = unwrapYAMLNode(node)
	schemaType, _ := schema["type"].(string)
	switch schemaType {
	case "object":
		return missingObjectFields(node, schema, path)
	case "array":
		return missingArrayFields(node, schema, path)
	default:
		return nil
	}
}

// unwrapYAMLNode resolves a document node to its content and normalizes an
// explicit YAML null to a nil node, so callers see one absent representation.
func unwrapYAMLNode(node *yaml.Node) *yaml.Node {
	if isYAMLNull(node) {
		return nil
	}
	if node == nil || node.Kind != yaml.DocumentNode {
		return node
	}
	if len(node.Content) == 0 {
		return nil
	}
	return node.Content[0]
}

// missingObjectFields reports the required fields absent from an object node.
// An absent node means every required field is missing.
func missingObjectFields(node *yaml.Node, schema map[string]any, path string) []string {
	var missing []string
	if node == nil {
		for _, key := range schemaRequiredFields(schema) {
			missing = append(missing, yamlFieldPath(path, key))
		}
		return missing
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for _, key := range schemaRequiredFields(schema) {
		if isYAMLNull(yamlMappingValue(node, key)) {
			missing = append(missing, yamlFieldPath(path, key))
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for i := 0; i+1 < len(node.Content); i += 2 {
		childSchema, ok := properties[node.Content[i].Value].(map[string]any)
		if !ok {
			continue
		}
		missing = append(missing, missingRequiredYAMLFields(node.Content[i+1], childSchema, yamlFieldPath(path, node.Content[i].Value))...)
	}
	return missing
}

func missingArrayFields(node *yaml.Node, schema map[string]any, path string) []string {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	itemSchema, ok := schema["items"].(map[string]any)
	if !ok {
		return nil
	}
	var missing []string
	for i, item := range node.Content {
		missing = append(missing, missingRequiredYAMLFields(item, itemSchema, fmt.Sprintf("%s[%d]", path, i))...)
	}
	return missing
}

func schemaRequiredFields(schema map[string]any) []string {
	fields, _ := schema["required"].([]string)
	return fields
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func isYAMLNull(node *yaml.Node) bool {
	return node == nil || node.Kind == 0 || node.Tag == "!!null"
}

func yamlFieldPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}

func require(result *ValidationResult, ok bool, format string, args ...any) {
	if ok {
		return
	}
	result.Errors = append(result.Errors, fmt.Sprintf(format, args...))
}
