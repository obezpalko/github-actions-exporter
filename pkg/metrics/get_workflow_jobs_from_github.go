package metrics

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/go-github/v45/github"
)

var (
	processedRuns   = make(map[int64]time.Time)
	processedRunsMu sync.Mutex

	// completedRunCh receives completed runs from getWorkflowRunsFromGithub
	// so getWorkflowJobsFromGithub doesn't need a separate API scan.
	completedRunCh = make(chan completedRun, 1000)
)

type completedRun struct {
	owner        string
	repo         string
	runID        int64
	createdAt    time.Time
	workflowName string
}

var setupSteps = map[string]bool{
	"Set up job":    true,
	"Set up runner": true,
}

func getJobsForRun(owner, repo string, runID int64) []*github.WorkflowJob {
	opt := &github.ListWorkflowJobsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}

	var jobs []*github.WorkflowJob
	for {
		resp, rr, err := client.Actions.ListWorkflowJobs(context.Background(), owner, repo, runID, opt)
		if rl_err, ok := err.(*github.RateLimitError); ok {
			log.Printf("ListWorkflowJobs ratelimited. Pausing until %s", rl_err.Rate.Reset.Time.String())
			time.Sleep(time.Until(rl_err.Rate.Reset.Time))
			continue
		} else if err != nil {
			log.Printf("ListWorkflowJobs error for repo %s/%s run %d: %s", owner, repo, runID, err.Error())
			return jobs
		}
		jobs = append(jobs, resp.Jobs...)
		if rr.NextPage == 0 {
			break
		}
		opt.Page = rr.NextPage
	}
	return jobs
}

func evictOldProcessedRuns() {
	cutoff := time.Now().Add(-1 * time.Hour)
	processedRunsMu.Lock()
	defer processedRunsMu.Unlock()
	for id, t := range processedRuns {
		if t.Before(cutoff) {
			delete(processedRuns, id)
		}
	}
}

func isProcessed(runID int64) bool {
	processedRunsMu.Lock()
	defer processedRunsMu.Unlock()
	_, ok := processedRuns[runID]
	return ok
}

func markProcessed(runID int64) {
	processedRunsMu.Lock()
	defer processedRunsMu.Unlock()
	processedRuns[runID] = time.Now()
}


func getWorkflowJobsFromGithub() {
	evictTicker := time.NewTicker(time.Hour)
	defer evictTicker.Stop()

	for {
		select {
		case <-evictTicker.C:
			evictOldProcessedRuns()
		case run := <-completedRunCh:
			if isProcessed(run.runID) {
				continue
			}
			repo := run.owner + "/" + run.repo
			jobs := getJobsForRun(run.owner, run.repo, run.runID)
			log.Printf("Processing %d jobs for run %d in %s", len(jobs), run.runID, repo)

			for _, job := range jobs {
				if job.StartedAt == nil {
					continue
				}
				labels := []string{
					repo,
					run.workflowName,
					job.GetName(),
					job.GetConclusion(),
				}

				queueSecs := job.StartedAt.Sub(run.createdAt).Seconds()
				jobQueueDurationHist.WithLabelValues(labels...).Observe(queueSecs)

				if job.CompletedAt != nil {
					runSecs := job.CompletedAt.Sub(job.StartedAt.Time).Seconds()
					jobRunDurationHist.WithLabelValues(labels...).Observe(runSecs)

					for _, step := range job.Steps {
						if !setupSteps[step.GetName()] {
							continue
						}
						if step.StartedAt == nil || step.CompletedAt == nil {
							continue
						}
						stepSecs := step.CompletedAt.Sub(step.StartedAt.Time).Seconds()
						jobStepDurationHist.WithLabelValues(append(labels, step.GetName())...).Observe(stepSecs)
					}
				}
			}
			markProcessed(run.runID)
		}
	}
}
