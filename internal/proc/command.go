package proc

// Command is an execution plan suitable for exec.Command plus cmd.Dir.
type Command struct {
	WorkDir string   `json:"work_dir"`
	Argv    []string `json:"argv"`
	Stdin   string   `json:"stdin,omitempty"`
	// EnvAppend adds Galley-owned entries without exposing them in evidence.
	EnvAppend []string `json:"-"`
	// EnvRemove strips inherited variables before EnvAppend is applied.
	EnvRemove []string `json:"-"`
	Warnings  []string `json:"warnings,omitempty"`
}
