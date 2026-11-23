package deploytest

import "github.com/hashicorp/nomad/api"

// FinishOnRegister plays the life of a one-off batch job a test can't register itself, an archive job of
// an orphan volume (feature 045): each time a job with this ID is registered, it ends as FinishRun says
// (ok, and the task that failed), with the output the function returns for the job as registered, so a
// test can answer with the values the job was given. The output is written before the job is dead, so a
// reader that sees the copy end finds it. Without it the job stays without a copy, as one on a node that
// isn't placing work does.
func (n *Nomad) FinishOnRegister(jobID string, ok bool, failedTask string, output func(job *api.Job) (stdout, stderr string)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.onRegister == nil {
		n.onRegister = map[string]func(*api.Job){}
	}
	n.onRegister[jobID] = n.finishing(ok, failedTask, output)
}

// FinishOnRegisterPrefix is FinishOnRegister for every job whose ID starts with prefix: the copy and check
// jobs of feature 052, whose IDs have a random part. The first hook whose prefix matches runs, in the order
// they were set. The function sees the job as registered and, through Variable, its own variable.
func (n *Nomad) FinishOnRegisterPrefix(prefix string, ok bool, failedTask string, output func(job *api.Job) (stdout, stderr string)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onRegisterPrefix = append(n.onRegisterPrefix, prefixHook{prefix: prefix, fn: n.finishing(ok, failedTask, output)})
}

// prefixHook is a FinishOnRegisterPrefix entry.
type prefixHook struct {
	prefix string
	fn     func(job *api.Job)
}

// finishing is the life of a one-off batch job that ends as FinishRun says, with the output the function
// returns for the job as registered.
func (n *Nomad) finishing(ok bool, failedTask string, output func(job *api.Job) (stdout, stderr string)) func(job *api.Job) {
	return func(job *api.Job) {
		jobID := *job.ID
		allocID := jobID + "-alloc"
		stdout, stderr := output(job)
		if stdout != "" {
			n.WriteLog(allocID, "stdout", stdout)
		}
		if stderr != "" {
			n.WriteLog(allocID, "stderr", stderr)
		}
		n.FinishRun(jobID, ok, failedTask, "", "")
	}
}
