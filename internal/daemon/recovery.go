package daemon

import (
	"os"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/queue"
)

// recoverInterruptedRunningTasks requeues running tasks left behind by a daemon
// that exited without finishing them. It runs once at startup so an interrupted
// task does not wait for the claim TTL before becoming eligible again, while a
// task still owned by a verified live daemon is left untouched.
func recoverInterruptedRunningTasks(root string) error {
	return queue.RecoverInterruptedRunning(root, ownerDaemonIsLive)
}

// ownerDaemonIsLive reports whether the daemon that recorded owner is still
// alive and is verifiably a Galley daemon process. Anything that cannot be
// positively verified is treated as not live so the interrupted task recovers.
func ownerDaemonIsLive(owner queue.Owner) (bool, error) {
	if owner.PID <= 0 || owner.PID == os.Getpid() {
		return false, nil
	}
	alive, err := daemonctl.Alive(owner.PID)
	if err != nil || !alive {
		// An inconclusive liveness probe is treated as not live; recovery is safe.
		//nolint:nilerr // inconclusive probe means not live
		return false, nil
	}
	info, err := daemonctl.ProcessInfo(owner.PID)
	if err != nil {
		//nolint:nilerr // inconclusive probe means not live
		return false, nil
	}
	if owner.ProcessStartedAt != "" && info.StartedAt != "" && owner.ProcessStartedAt != info.StartedAt {
		// The PID has been recycled by an unrelated process since the claim.
		return false, nil
	}
	command := strings.ToLower(info.Command + " " + info.Executable)
	if !strings.Contains(command, "galley") {
		return false, nil
	}
	return true, nil
}

// currentRunningOwner describes this daemon process for the owner sidecar of a
// task it just claimed.
func currentRunningOwner() queue.Owner {
	pid := os.Getpid()
	started := ""
	if info, err := daemonctl.ProcessInfo(pid); err == nil {
		started = info.StartedAt
	}
	return queue.Owner{
		PID:              pid,
		ProcessStartedAt: started,
		RecordedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
}
