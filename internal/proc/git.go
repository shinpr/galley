package proc

// GitArgs adds the cross-platform core.longpaths prefix to every Galley-owned
// git command, defaulting an empty executable to "git".
func GitArgs(gitBin string, args ...string) []string {
	bin := gitBin
	if bin == "" {
		bin = "git"
	}
	out := make([]string, 0, len(args)+3)
	out = append(out, bin, "-c", "core.longpaths=true")
	out = append(out, args...)
	return out
}
