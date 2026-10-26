package inspect

// Unreachable recover needs consecutive Probe OK so a single blip does not
// clear alert_sent. Fire stays immediate: full diagnose is ~2h apart, and
// recovery probes only run after something is already open — N>=2 would delay
// the first Unreachable Feishu by another full round.
const unreachableOKNeed = 2

type reachStreak struct {
	ok int
}

// noteReach updates the per-cluster Probe streak.
// fire is true on any Probe failure (emit Unreachable when appropriate).
// recover is true after M consecutive Probe successes (close Unreachable / ClientError).
func (w *Worker) noteReach(clusterID string, probeOK bool) (fire, recover bool) {
	if w == nil || clusterID == "" {
		if probeOK {
			return false, true
		}
		return true, false
	}
	w.reachMu.Lock()
	defer w.reachMu.Unlock()
	if w.reach == nil {
		w.reach = map[string]*reachStreak{}
	}
	st := w.reach[clusterID]
	if st == nil {
		st = &reachStreak{}
		w.reach[clusterID] = st
	}
	if probeOK {
		st.ok++
		return false, st.ok >= unreachableOKNeed
	}
	st.ok = 0
	return true, false
}

// dropReach clears streak state when the cluster is removed.
func (w *Worker) dropReach(clusterID string) {
	if w == nil || clusterID == "" {
		return
	}
	w.reachMu.Lock()
	defer w.reachMu.Unlock()
	delete(w.reach, clusterID)
}
