package metrics

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v45/github"
	"github.com/spendesk/github-actions-exporter/pkg/config"
)

var (
	processedRuns   = make(map[int64]time.Time)
	processedRunsMu sync.Mutex
)

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

func getWorkflowName(repo string, workflowID int64) string {
	if r, ok := workflows[repo]; ok {
		if w, ok := r[workflowID]; ok {
			return *w.Name
		}
	}
	return "unknown"
}

func getWorkflowJobsFromGithub() {
	// On startup, only look back 2 refresh cycles to avoid re-processing old runs.
	startupCutoff := time.Now().Add(-2 * time.Duration(config.Github.Refresh) * time.Second)

	for {
		evictOldProcessedRuns()

		for _, repo := range repositories {
			r := strings.Split(repo, "/")
			runs := getRecentWorkflowRuns(r[0], r[1])

			for _, run := range runs {
				// On the first iteration, skip runs older than the startup cutoff.
				if run.CreatedAt != nil && run.CreatedAt.Time.Before(startupCutoff) {
					continue
				}
				// Only process completed runs; skip in-progress/queued.
				if run.GetStatus() != "completed" {
					continue
				}
				if isProcessed(*run.ID) {
					continue
				}

				workflowName := getWorkflowName(repo, *run.WorkflowID)
				runCreatedAt := run.CreatedAt.Time
				jobs := getJobsForRun(r[0], r[1], *run.ID)

				for _, job := range jobs {
					if job.StartedAt == nil {
						continue
					}

					labels := []string{
						repo,
						workflowName,
						job.GetName(),
						job.GetConclusion(),
					}

					queueSecs := job.StartedAt.Sub(runCreatedAt).Seconds()
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

				markProcessed(*run.ID)
			}
		}

		// After the first pass, clear the startup cutoff so subsequent passes
		// process all new completed runs regardless of age within the 12h window.
		startupCutoff = time.Time{}

		time.Sleep(time.Duration(config.Github.Refresh) * time.Second)
	}
}
