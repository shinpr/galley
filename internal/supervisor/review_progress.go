package supervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

type reviewContract struct {
	Goal                   string                         `json:"goal"`
	CWD                    string                         `json:"cwd"`
	AllowedPaths           []string                       `json:"allowed_paths"`
	ForbiddenPaths         []string                       `json:"forbidden_paths"`
	Permission             string                         `json:"permission"`
	Acceptance             []reviewAcceptanceContract     `json:"acceptance"`
	TaskFiles              []task.InputFile               `json:"task_files"`
	AcceptanceSkeleton     *task.AcceptanceSkeletonConfig `json:"acceptance_skeleton"`
	EnvironmentConstraints profile.Constraints            `json:"environment_constraints"`
	QualityID              string                         `json:"quality_id"`
	RequiredChecks         []profile.RequiredCheck        `json:"required_checks"`
	Quality                []profile.ReviewDimension      `json:"quality"`
	EvidenceRequirements   profile.EvidenceRequirements   `json:"evidence_requirements"`
	PassPolicy             profile.PassPolicy             `json:"pass_policy"`
	InputFilesDigest       string                         `json:"input_files_digest"`
}

// ReviewContractContext carries daemon-owned evidence that affects whether a
// persisted review pass remains valid.
type ReviewContractContext struct {
	SourceCWD        string
	InputFilesDigest string
}

type reviewAcceptanceContract struct {
	ID           string `json:"id"`
	Text         string `json:"text"`
	Verification string `json:"verification"`
}

// ReconcileReviewProgress drops stale or unknown passes and projects the
// supervisor-owned acceptance state onto the task's user-facing AC statuses.
func ReconcileReviewProgress(value *task.Task, profiles profile.Bundle) {
	ReconcileReviewProgressWithContext(value, profiles, ReviewContractContext{})
}

func ReconcileReviewProgressWithContext(value *task.Task, profiles profile.Bundle, context ReviewContractContext) {
	hash := reviewContractHash(*value, profiles, context)
	if value.ReviewProgress == nil || value.ReviewProgress.ContractHash != hash {
		value.ReviewProgress = &task.ReviewProgress{ContractHash: hash}
		for i := range value.AcceptanceCriteria {
			value.AcceptanceCriteria[i].Status = "pending"
		}
		return
	}

	value.ReviewProgress.Acceptance = orderedIDs(
		value.ReviewProgress.Acceptance,
		acceptanceIDs(*value),
	)
	value.ReviewProgress.Quality = orderedIDs(
		value.ReviewProgress.Quality,
		qualityIDs(profiles.Quality),
	)
	projectAcceptancePasses(value)
}

// ApplyReviewProgress replaces the persisted pass sets with the recognized IDs
// returned by the latest supervisor review.
func ApplyReviewProgress(value *task.Task, profiles profile.Bundle, verdict Verdict) {
	ApplyReviewProgressWithContext(value, profiles, ReviewContractContext{}, verdict)
}

func ApplyReviewProgressWithContext(value *task.Task, profiles profile.Bundle, context ReviewContractContext, verdict Verdict) {
	ReconcileReviewProgressWithContext(value, profiles, context)
	value.ReviewProgress.Acceptance = orderedIDs(verdict.AcceptancePasses, acceptanceIDs(*value))
	value.ReviewProgress.Quality = orderedIDs(verdict.QualityPasses, qualityIDs(profiles.Quality))
	projectAcceptancePasses(value)
	projectRevisionPasses(value, verdict.AcceptancePasses)
}

func reviewContractHash(value task.Task, profiles profile.Bundle, context ReviewContractContext) string {
	contract := buildReviewContract(value, profiles, context)
	var body bytes.Buffer
	writeTaskDirectionHash(&body, contract)
	writeAcceptanceReviewHash(&body, contract.Acceptance)
	writeTaskFilesHash(&body, contract.TaskFiles)
	writeAcceptanceSkeletonHash(&body, contract.AcceptanceSkeleton)
	writeEnvironmentReviewHash(&body, contract.EnvironmentConstraints)
	writeQualityReviewHash(&body, contract)
	sum := sha256.Sum256(body.Bytes())
	return hex.EncodeToString(sum[:])
}

func buildReviewContract(value task.Task, profiles profile.Bundle, context ReviewContractContext) reviewContract {
	cwd := context.SourceCWD
	if cwd == "" {
		cwd = value.Scope.CWD
	}
	contract := reviewContract{
		Goal:             value.Goal,
		CWD:              cwd,
		AllowedPaths:     value.Scope.AllowedPaths,
		ForbiddenPaths:   value.Scope.ForbiddenPaths,
		Permission:       value.Scope.Permission,
		Acceptance:       make([]reviewAcceptanceContract, 0, len(value.AcceptanceCriteria)),
		TaskFiles:        value.Files,
		InputFilesDigest: context.InputFilesDigest,
	}
	for _, criterion := range value.AcceptanceCriteria {
		contract.Acceptance = append(contract.Acceptance, reviewAcceptanceContract{
			ID: criterion.ID, Text: criterion.Text, Verification: criterion.Verification,
		})
	}
	if value.Preflight != nil && value.Preflight.AcceptanceSkeleton.IsEnabled() {
		contract.AcceptanceSkeleton = value.Preflight.AcceptanceSkeleton
	}
	if profiles.Environment != nil {
		contract.EnvironmentConstraints = profiles.Environment.Constraints
	}
	if profiles.Quality != nil {
		contract.QualityID = profiles.Quality.ID
		contract.RequiredChecks = profiles.Quality.RequiredChecks
		contract.Quality = profiles.Quality.ReviewDimensions
		contract.EvidenceRequirements = profiles.Quality.EvidenceRequirements
		contract.PassPolicy = profiles.Quality.PassPolicy
	}
	return contract
}

func writeTaskDirectionHash(body *bytes.Buffer, contract reviewContract) {
	writeReviewHashValue(body, contract.Goal)
	writeReviewHashValue(body, contract.CWD)
	writeReviewHashValue(body, strconv.Itoa(len(contract.AllowedPaths)))
	for _, path := range contract.AllowedPaths {
		writeReviewHashValue(body, path)
	}
	writeReviewHashValue(body, strconv.Itoa(len(contract.ForbiddenPaths)))
	for _, path := range contract.ForbiddenPaths {
		writeReviewHashValue(body, path)
	}
	writeReviewHashValue(body, contract.Permission)
	writeReviewHashValue(body, contract.InputFilesDigest)
}

func writeAcceptanceReviewHash(body *bytes.Buffer, acceptance []reviewAcceptanceContract) {
	writeReviewHashValue(body, strconv.Itoa(len(acceptance)))
	for _, criterion := range acceptance {
		writeReviewHashValue(body, criterion.ID)
		writeReviewHashValue(body, criterion.Text)
		writeReviewHashValue(body, criterion.Verification)
	}
}

func writeQualityReviewHash(body *bytes.Buffer, contract reviewContract) {
	writeReviewHashValue(body, contract.QualityID)
	writeReviewHashValue(body, strconv.Itoa(len(contract.RequiredChecks)))
	for _, check := range contract.RequiredChecks {
		writeReviewHashValue(body, check.ID)
		writeReviewHashValue(body, strconv.FormatBool(check.Required))
		writeReviewHashValue(body, strconv.Itoa(len(check.PreferredCommands)))
		for _, command := range check.PreferredCommands {
			writeReviewHashValue(body, command)
		}
	}
	writeReviewHashValue(body, strconv.Itoa(len(contract.Quality)))
	for _, dimension := range contract.Quality {
		writeReviewHashValue(body, dimension.ID)
		writeReviewHashValue(body, strconv.Itoa(dimension.Weight))
		writeReviewHashValue(body, strconv.FormatBool(dimension.Required))
		writeReviewHashValue(body, dimension.Pass)
	}
	writeReviewHashValue(body, strconv.FormatBool(contract.EvidenceRequirements.FileLineReferences))
	writeReviewHashValue(body, strconv.FormatBool(contract.EvidenceRequirements.CommandOutputs))
	writeReviewHashValue(body, strconv.FormatBool(contract.PassPolicy.RequiredDimensionsMustPass))
	writeReviewHashValue(body, strconv.Itoa(contract.PassPolicy.MinScore))
	writeReviewHashValue(body, strconv.Itoa(len(contract.PassPolicy.BlockingSeverities)))
	for _, severity := range contract.PassPolicy.BlockingSeverities {
		writeReviewHashValue(body, severity)
	}
}

func writeTaskFilesHash(body *bytes.Buffer, files []task.InputFile) {
	writeReviewHashValue(body, strconv.Itoa(len(files)))
	for _, file := range files {
		writeReviewHashValue(body, file.Source)
		writeReviewHashValue(body, file.Destination)
		writeReviewHashValue(body, file.Description)
		writeReviewHashValue(body, strconv.FormatBool(file.Commit))
	}
}

func writeAcceptanceSkeletonHash(body *bytes.Buffer, config *task.AcceptanceSkeletonConfig) {
	writeReviewHashValue(body, strconv.FormatBool(config != nil))
	if config == nil {
		return
	}
	writeReviewHashValue(body, strconv.Itoa(len(config.Outputs)))
	for _, output := range config.Outputs {
		writeReviewHashValue(body, output.ACID)
		writeReviewHashValue(body, output.Path)
		writeReviewHashValue(body, output.Kind)
		writeReviewHashValue(body, output.Purpose)
		writeReviewHashValue(body, output.Satisfies)
		writeReviewHashValue(body, output.IntegrationPoint)
		writeReviewHashValue(body, strconv.FormatBool(output.ImplementationRequired))
		writeReviewHashValue(body, output.Template)
	}
}

func writeEnvironmentReviewHash(body *bytes.Buffer, constraints profile.Constraints) {
	writeReviewHashValue(body, constraints.Network)
	writeReviewHashValue(body, constraints.SecretsPolicy)
	writeReviewHashValue(body, constraints.DestructiveCommands)
}

func writeReviewHashValue(body *bytes.Buffer, value string) {
	body.WriteString(strconv.Itoa(len(value)))
	body.WriteByte(':')
	body.WriteString(value)
}

func acceptanceIDs(value task.Task) []string {
	ids := make([]string, 0, len(value.AcceptanceCriteria))
	for _, criterion := range value.AcceptanceCriteria {
		ids = append(ids, criterion.ID)
	}
	return ids
}

func acceptanceIDSet(value task.Task) map[string]struct{} {
	ids := make(map[string]struct{}, len(value.AcceptanceCriteria))
	for _, criterion := range value.AcceptanceCriteria {
		ids[criterion.ID] = struct{}{}
	}
	return ids
}

func qualityIDs(quality *profile.Quality) []string {
	if quality == nil {
		return nil
	}
	ids := make([]string, 0, len(quality.ReviewDimensions))
	for _, dimension := range quality.ReviewDimensions {
		ids = append(ids, dimension.ID)
	}
	return ids
}

func orderedIDs(current []string, ids []string) []string {
	return idsInOrder(stringSet(current), ids)
}

func idsInOrder(current map[string]bool, ids []string) []string {
	passed := make([]string, 0, len(current))
	for _, id := range ids {
		if current[id] {
			passed = append(passed, id)
		}
	}
	return passed
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if id := strings.TrimSpace(value); id != "" {
			set[id] = true
		}
	}
	return set
}

func projectAcceptancePasses(value *task.Task) {
	passed := stringSet(value.ReviewProgress.Acceptance)
	for i := range value.AcceptanceCriteria {
		criterion := &value.AcceptanceCriteria[i]
		if passed[criterion.ID] {
			criterion.Status = "satisfied"
		} else {
			criterion.Status = "pending"
		}
	}
}

func projectRevisionPasses(value *task.Task, passes []string) {
	passed := stringSet(passes)
	for i := range value.RevisionRequests {
		request := &value.RevisionRequests[i]
		if passed["revision:"+request.ID] {
			request.Status = "addressed"
			request.Evidence = "Verified by the supervisor."
		}
	}
}
