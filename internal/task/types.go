package task

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Task is the top-level Galley task YAML contract.
type Task struct {
	ID                 string                `yaml:"id" json:"id"`
	Mode               string                `yaml:"mode" json:"mode"`
	Status             string                `yaml:"status" json:"status"`
	Goal               string                `yaml:"goal" json:"goal"`
	AcceptanceCriteria []AcceptanceCriterion `yaml:"acceptance_criteria" json:"acceptance_criteria"`
	Files              []InputFile           `yaml:"files,omitempty" json:"files,omitempty"`
	Scope              Scope                 `yaml:"scope" json:"scope"`
	ExecutionPolicy    ExecutionPolicy       `yaml:"execution_policy" json:"execution_policy"`
	Worktree           Worktree              `yaml:"worktree" json:"worktree"`
	Supervisor         Supervisor            `yaml:"supervisor" json:"supervisor"`
	Executor           Executor              `yaml:"executor" json:"executor"`
	Preflight          *Preflight            `yaml:"preflight,omitempty" json:"preflight,omitempty"`
	Decisions          []Decision            `yaml:"decisions" json:"decisions"`
	Risks              []Risk                `yaml:"risks" json:"risks"`
	DiscussionItems    []DiscussionItem      `yaml:"discussion_items,omitempty" json:"discussion_items,omitempty"`
	RevisionRequests   []RevisionRequest     `yaml:"revision_requests,omitempty" json:"revision_requests,omitempty"`
	Attempts           []Attempt             `yaml:"attempts" json:"attempts"`
	Verification       Verification          `yaml:"verification" json:"verification"`
	PR                 PR                    `yaml:"pr" json:"pr"`
}

// Preflight groups optional pre-executor stages. A nil pointer or absent
// fields means the stage is disabled and the daemon flow is unchanged.
type Preflight struct {
	AcceptanceSkeleton *AcceptanceSkeletonConfig `yaml:"acceptance_skeleton,omitempty" json:"acceptance_skeleton,omitempty"`
}

// AcceptanceSkeletonConfig configures the optional acceptance skeleton
// preflight stage that materializes AC-linked test skeletons in the worktree
// before the first executor attempt.
type AcceptanceSkeletonConfig struct {
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	Mode         string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Required     *bool    `yaml:"required,omitempty" json:"required,omitempty"`
	AllowedPaths []string `yaml:"allowed_paths,omitempty" json:"allowed_paths,omitempty"`
	// Outputs is daemon-owned runtime metadata. Authors opt in with enabled:true;
	// the built-in creator writes these bindings back before the executor runs.
	Outputs []AcceptanceSkeletonOutputDef `yaml:"outputs,omitempty" json:"outputs,omitempty"`
}

// AcceptanceSkeletonOutputDef declares one skeleton file the preflight stage
// must materialize in the worktree before the first executor attempt. Each
// entry binds an AC ID to a relative path, a kind/purpose pair that documents
// what the skeleton verifies, and an implementation_required flag that tells
// the supervisor this file must be completed rather than left as a placeholder.
type AcceptanceSkeletonOutputDef struct {
	ACID                   string `yaml:"ac_id" json:"ac_id"`
	Path                   string `yaml:"path" json:"path"`
	Kind                   string `yaml:"kind" json:"kind"`
	Purpose                string `yaml:"purpose" json:"purpose"`
	Satisfies              string `yaml:"satisfies,omitempty" json:"satisfies,omitempty"`
	IntegrationPoint       string `yaml:"integration_point,omitempty" json:"integration_point,omitempty"`
	ImplementationRequired bool   `yaml:"implementation_required" json:"implementation_required"`
	Template               string `yaml:"template,omitempty" json:"template,omitempty"`
}

// IsEnabled reports whether the acceptance skeleton stage should run.
func (c *AcceptanceSkeletonConfig) IsEnabled() bool {
	return c != nil && c.Enabled
}

// IsRequired reports whether the stage requires every AC to have a skeleton
// output or no_skeletons reason. Required quality-check evidence still gates
// acceptance whenever the stage is enabled.
// Defaults to true when the stage is enabled.
func (c *AcceptanceSkeletonConfig) IsRequired() bool {
	if c == nil || !c.Enabled {
		return false
	}
	if c.Required == nil {
		return true
	}
	return *c.Required
}

// InputFile describes a source file Galley should place in the execution workspace.
type InputFile struct {
	Source      string `yaml:"source" json:"source"`
	Destination string `yaml:"destination" json:"destination"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Commit      bool   `yaml:"commit" json:"commit"`
}

// AcceptanceCriterion describes one observable completion requirement. The
// Verification field is guidance for evidence collection; required runnable
// checks are configured through profiles and recorded under Task.Verification.
type AcceptanceCriterion struct {
	ID           string `yaml:"id" json:"id"`
	Text         string `yaml:"text" json:"text"`
	Verification string `yaml:"verification" json:"verification"`
	Status       string `yaml:"status" json:"status"`
}

// Scope constrains where and how a task may operate.
type Scope struct {
	CWD            string   `yaml:"cwd" json:"cwd"`
	AllowedPaths   []string `yaml:"allowed_paths" json:"allowed_paths"`
	ForbiddenPaths []string `yaml:"forbidden_paths" json:"forbidden_paths"`
	Permission     string   `yaml:"permission" json:"permission"`
}

// ExecutionPolicy describes loop, timeout, and escalation behavior.
type ExecutionPolicy struct {
	LoopBudget                       LoopBudget `yaml:"loop_budget" json:"loop_budget"`
	TimeoutMS                        int        `yaml:"timeout_ms" json:"timeout_ms"`
	AFKDecisionPolicy                string     `yaml:"afk_decision_policy" json:"afk_decision_policy"`
	StopOnDestructiveOperation       bool       `yaml:"stop_on_destructive_operation" json:"stop_on_destructive_operation"`
	StopOnMissingSecret              bool       `yaml:"stop_on_missing_secret" json:"stop_on_missing_secret"`
	StopOnExternalServiceUnavailable bool       `yaml:"stop_on_external_service_unavailable" json:"stop_on_external_service_unavailable"`
}

// LoopBudget is a non-negative attempt count. A count of zero means unlimited.
type LoopBudget struct {
	Count int  `json:"count,omitempty"`
	Set   bool `yaml:"-" json:"-"`
}

// UnmarshalYAML accepts an integer. Zero means unlimited.
func (b *LoopBudget) UnmarshalYAML(value *yaml.Node) error {
	b.Set = true
	if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
		return fmt.Errorf("loop_budget must be an integer >= 0; use 0 for unlimited")
	}
	var count int
	if err := value.Decode(&count); err != nil {
		return err
	}
	b.Count = count
	return nil
}

// MarshalYAML encodes the budget as an integer.
func (b LoopBudget) MarshalYAML() (any, error) {
	if !b.Set {
		return DefaultLoopBudget, nil
	}
	return b.Count, nil
}

// String formats the budget for prompts and diagnostics.
func (b LoopBudget) String() string {
	if !b.Set {
		return fmt.Sprintf("%d", DefaultLoopBudget)
	}
	if b.Count == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", b.Count)
}

// Worktree describes the optional isolated workspace for AFK execution.
type Worktree struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Branch  string `yaml:"branch" json:"branch"`
	Path    string `yaml:"path" json:"path"`
}

// Supervisor configures the review authority for a task.
type Supervisor struct {
	ReviewIterations int `yaml:"review_iterations" json:"review_iterations"`
}

// Executor configures the implementation worker for a task.
type Executor struct {
	CLI           string  `yaml:"cli" json:"cli"`
	Model         string  `yaml:"model" json:"model"`
	Effort        string  `yaml:"effort" json:"effort"`
	PromptProfile string  `yaml:"prompt_profile" json:"prompt_profile"`
	PromptMode    string  `yaml:"prompt_mode" json:"prompt_mode"`
	MaxBudgetUSD  float64 `yaml:"max_budget_usd" json:"max_budget_usd"`
}

// Decision records an ambiguity resolved during execution.
type Decision struct {
	ID               string `yaml:"id" json:"id"`
	Question         string `yaml:"question" json:"question"`
	Chosen           string `yaml:"chosen" json:"chosen"`
	Rationale        string `yaml:"rationale" json:"rationale"`
	Reversibility    string `yaml:"reversibility" json:"reversibility"`
	NeedsHumanReview bool   `yaml:"needs_human_review" json:"needs_human_review"`
}

// Risk records uncertainty or incomplete verification that remains after execution.
type Risk struct {
	ID                   string `yaml:"id" json:"id"`
	Type                 string `yaml:"type" json:"type"`
	Detail               string `yaml:"detail" json:"detail"`
	Mitigation           string `yaml:"mitigation" json:"mitigation"`
	HumanReviewSuggested bool   `yaml:"human_review_suggested" json:"human_review_suggested"`
}

// DiscussionItem records accepted-work feedback that does not change the acceptance verdict.
type DiscussionItem struct {
	ID                    string `yaml:"id" json:"id"`
	Topic                 string `yaml:"topic" json:"topic"`
	Summary               string `yaml:"summary" json:"summary"`
	RequiresHumanDecision bool   `yaml:"requires_human_decision" json:"requires_human_decision"`
}

// RevisionRequest records a PR comment or review instruction that must be addressed before acceptance.
type RevisionRequest struct {
	ID        string `yaml:"id" json:"id"`
	Source    string `yaml:"source" json:"source"`
	CommentID string `yaml:"comment_id,omitempty" json:"comment_id,omitempty"`
	Text      string `yaml:"text" json:"text"`
	Status    string `yaml:"status" json:"status"`
	Evidence  string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

// Attempt records one executor and supervisor loop iteration.
type Attempt struct {
	Number            int           `yaml:"number" json:"number"`
	StartedAt         string        `yaml:"started_at" json:"started_at"`
	CompletedAt       string        `yaml:"completed_at" json:"completed_at"`
	ClaudeStatus      string        `yaml:"claude_status" json:"claude_status"`
	SupervisorVerdict string        `yaml:"supervisor_verdict" json:"supervisor_verdict"`
	Summary           string        `yaml:"summary" json:"summary"`
	Error             *AttemptError `yaml:"error,omitempty" json:"error,omitempty"`
}

// AttemptError records the operator-facing failure that ended an attempt.
type AttemptError struct {
	Phase       string `yaml:"phase" json:"phase"`
	Kind        string `yaml:"kind" json:"kind"`
	Message     string `yaml:"message" json:"message"`
	ArtifactDir string `yaml:"artifact_dir,omitempty" json:"artifact_dir,omitempty"`
}

// Verification contains command evidence collected for a task.
type Verification struct {
	Commands []VerificationCommand `yaml:"commands" json:"commands"`
}

// VerificationCommand records the result of one verification command.
type VerificationCommand struct {
	Cmd           string `yaml:"cmd" json:"cmd"`
	Status        string `yaml:"status" json:"status"`
	OutputExcerpt string `yaml:"output_excerpt" json:"output_excerpt"`
}

// MarshalYAML keeps captured command output in a quoted scalar. Raw tool logs
// can contain JSONL and uneven leading whitespace, which yaml.v3 may otherwise
// encode as a block scalar that is not safely round-trippable.
func (c VerificationCommand) MarshalYAML() (any, error) {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			yamlStringNode("cmd", 0),
			yamlStringNode(c.Cmd, 0),
			yamlStringNode("status", 0),
			yamlStringNode(c.Status, 0),
			yamlStringNode("output_excerpt", 0),
			yamlStringNode(c.OutputExcerpt, yaml.DoubleQuotedStyle),
		},
	}, nil
}

func yamlStringNode(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: value,
		Style: style,
	}
}

// PR records pull request state for AFK workflows.
//
// AuthorLogin is the GitHub login of the user who owns the pull request and
// is captured at PR creation time. Galley restricts PR comment commands such
// as `/galley` to comments whose author matches AuthorLogin. The field is
// optional so older task YAML files load without breaking; task flows that
// need the trust check treat an empty AuthorLogin as "unknown" and fail
// closed.
type PR struct {
	URL                 string   `yaml:"url" json:"url"`
	Status              string   `yaml:"status" json:"status"`
	AuthorLogin         string   `yaml:"author_login,omitempty" json:"author_login,omitempty"`
	ProcessedCommentIDs []string `yaml:"processed_comment_ids,omitempty" json:"processed_comment_ids,omitempty"`
}

// ValidationResult contains task validation errors and warnings.
type ValidationResult struct {
	Task     Task     `json:"task"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// Valid reports whether validation produced no errors.
func (r ValidationResult) Valid() bool {
	return len(r.Errors) == 0
}
