package profile

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Quality describes review requirements that should shape executor verification.
type Quality struct {
	ID                   string               `yaml:"id" json:"id"`
	ReviewLoops          ReviewLoops          `yaml:"review_loops" json:"review_loops"`
	RequiredChecks       []RequiredCheck      `yaml:"required_checks" json:"required_checks"`
	ReviewDimensions     []ReviewDimension    `yaml:"review_dimensions" json:"review_dimensions"`
	EvidenceRequirements EvidenceRequirements `yaml:"evidence_requirements" json:"evidence_requirements"`
	PassPolicy           PassPolicy           `yaml:"pass_policy" json:"pass_policy"`
}

type ReviewLoops struct {
	DefaultMax int `yaml:"default_max" json:"default_max"`
	AFKMax     int `yaml:"afk_max" json:"afk_max"`
	HITLMax    int `yaml:"hitl_max" json:"hitl_max"`
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
	RequiredDimensionsMustPass bool `yaml:"required_dimensions_must_pass" json:"required_dimensions_must_pass"`
	MinScore                   int  `yaml:"min_score" json:"min_score"`
	UnresolvedHighAllowed      int  `yaml:"unresolved_high_findings_allowed" json:"unresolved_high_findings_allowed"`
}

// Environment describes local execution capabilities and constraints.
type Environment struct {
	ID          string            `yaml:"id" json:"id"`
	CWD         string            `yaml:"cwd" json:"cwd"`
	Commands    map[string]string `yaml:"commands" json:"commands"`
	Constraints Constraints       `yaml:"constraints" json:"constraints"`
}

type Constraints struct {
	Network             string `yaml:"network" json:"network"`
	SecretsPolicy       string `yaml:"secrets_policy" json:"secrets_policy"`
	DestructiveCommands string `yaml:"destructive_commands" json:"destructive_commands"`
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
	for i, check := range q.RequiredChecks {
		prefix := fmt.Sprintf("required_checks[%d]", i)
		require(&result, check.ID != "", "%s.id is required", prefix)
		if check.Required && len(check.PreferredCommands) == 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("%s.preferred_commands is required when check is required", prefix))
		}
	}
	for i, dim := range q.ReviewDimensions {
		prefix := fmt.Sprintf("review_dimensions[%d]", i)
		require(&result, dim.ID != "", "%s.id is required", prefix)
		require(&result, dim.Weight >= 0, "%s.weight cannot be negative", prefix)
		require(&result, dim.Pass != "", "%s.pass is required", prefix)
	}
	return result
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
	return result
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
