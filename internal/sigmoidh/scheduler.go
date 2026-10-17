package sigmoidh

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type taskScheduler struct {
	mu   sync.Mutex
	next int
	jobs map[int]*scheduledTask
}

type scheduledTask struct {
	id      int
	kind    string
	spec    string
	message string
	nextAt  time.Time
	stop    chan struct{}
}

func newTaskScheduler() *taskScheduler {
	return &taskScheduler{jobs: make(map[int]*scheduledTask)}
}

func (s *taskScheduler) add(job *scheduledTask, callback func(string)) int {
	s.mu.Lock()
	s.next++
	job.id = s.next
	job.stop = make(chan struct{})
	s.jobs[job.id] = job
	s.mu.Unlock()
	if job.kind == "timer" {
		go func() {
			timer := time.NewTimer(time.Until(job.nextAt))
			defer timer.Stop()
			select {
			case <-job.stop:
				return
			case <-timer.C:
				s.mu.Lock()
				delete(s.jobs, job.id)
				s.mu.Unlock()
				callback(job.message)
			}
		}()
	} else {
		go s.runCron(job, callback)
	}
	return job.id
}

func (s *taskScheduler) AddTimer(after time.Duration, message string, callback func(string)) int {
	return s.add(&scheduledTask{kind: "timer", spec: after.String(), message: message, nextAt: time.Now().Add(after)}, callback)
}

func (s *taskScheduler) AddCron(expression, message string, callback func(string)) int {
	next, err := nextCron(expression, time.Now())
	if err != nil {
		return 0
	}
	return s.add(&scheduledTask{kind: "cron", spec: expression, message: message, nextAt: next}, callback)
}

func (s *taskScheduler) runCron(job *scheduledTask, callback func(string)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastMinute := time.Now().Format("2006-01-02 15:04")
	for {
		select {
		case <-job.stop:
			return
		case now := <-ticker.C:
			minute := now.Format("2006-01-02 15:04")
			if minute == lastMinute {
				continue
			}
			lastMinute = minute
			matches, err := cronMatches(job.spec, now)
			if err != nil || !matches {
				continue
			}
			s.mu.Lock()
			if s.jobs[job.id] != job {
				s.mu.Unlock()
				return
			}
			if next, err := nextCron(job.spec, now); err == nil {
				job.nextAt = next
			}
			s.mu.Unlock()
			callback(job.message)
		}
	}
}

func (s *taskScheduler) Cancel(id int) bool {
	s.mu.Lock()
	job := s.jobs[id]
	if job != nil {
		delete(s.jobs, id)
		close(job.stop)
	}
	s.mu.Unlock()
	return job != nil
}

func (s *taskScheduler) Status() string {
	s.mu.Lock()
	jobs := make([]scheduledTask, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, *job)
	}
	s.mu.Unlock()
	if len(jobs) == 0 {
		return "none"
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].id < jobs[j].id })
	parts := make([]string, 0, len(jobs))
	for _, job := range jobs {
		parts = append(parts, fmt.Sprintf("#%d %s at %s: %s", job.id, job.kind, job.nextAt.Format(time.RFC3339), job.message))
	}
	return strings.Join(parts, "; ")
}

func (s *taskScheduler) Close() {
	s.mu.Lock()
	for id, job := range s.jobs {
		delete(s.jobs, id)
		close(job.stop)
	}
	s.mu.Unlock()
}

type cronField struct {
	any    bool
	values map[int]bool
}

type cronSpec [5]cronField

var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

func parseCron(expression string) (cronSpec, error) {
	var spec cronSpec
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return spec, fmt.Errorf("cron expression needs five fields: minute hour day month weekday")
	}
	for i, part := range parts {
		field := cronField{values: make(map[int]bool)}
		for _, item := range strings.Split(part, ",") {
			if item == "*" {
				field.any = true
				continue
			}
			step := 1
			base := item
			hasStep := false
			if before, after, ok := strings.Cut(item, "/"); ok {
				base = before
				hasStep = true
				var err error
				step, err = strconv.Atoi(after)
				if err != nil || step < 1 {
					return spec, fmt.Errorf("invalid cron step %q", item)
				}
			}
			low, high := cronBounds[i][0], cronBounds[i][1]
			if base != "*" {
				if start, end, isRange := strings.Cut(base, "-"); isRange {
					var err error
					low, err = strconv.Atoi(start)
					if err == nil {
						high, err = strconv.Atoi(end)
					}
					if err != nil || low < cronBounds[i][0] || high > cronBounds[i][1] || high < low {
						return spec, fmt.Errorf("invalid cron range %q for field %d", base, i+1)
					}
				} else {
					value, err := strconv.Atoi(base)
					if err != nil || value < low || value > high {
						return spec, fmt.Errorf("cron value %q is out of range for field %d", base, i+1)
					}
					low, high = value, value
					if hasStep {
						high = cronBounds[i][1]
					}
				}
			}
			for value := low; value <= high; value += step {
				if i == 4 && value == 7 { // Sunday may be written as 0 or 7.
					field.values[0] = true
				} else {
					field.values[value] = true
				}
			}
		}
		spec[i] = field
	}
	return spec, nil
}

func (f cronField) matches(value int) bool { return f.any || f.values[value] }

func cronMatches(expression string, t time.Time) (bool, error) {
	spec, err := parseCron(expression)
	if err != nil {
		return false, err
	}
	return cronSpecMatches(spec, t), nil
}

func cronSpecMatches(spec cronSpec, t time.Time) bool {
	weekday := int(t.Weekday())
	dayOfMonth, dayOfWeek := spec[2].matches(t.Day()), spec[4].matches(weekday)
	if !spec[2].any && !spec[4].any {
		dayOfMonth = dayOfMonth || dayOfWeek // standard cron OR rule when both are restricted
	} else {
		dayOfMonth = dayOfMonth && dayOfWeek
	}
	return spec[0].matches(t.Minute()) && spec[1].matches(t.Hour()) && dayOfMonth && spec[3].matches(int(t.Month()))
}

func nextCron(expression string, after time.Time) (time.Time, error) {
	spec, err := parseCron(expression)
	if err != nil {
		return time.Time{}, err
	}
	candidate := after.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(1, 0, 0)
	for candidate.Before(limit) {
		if cronSpecMatches(spec, candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron expression has no occurrence in the next year")
}
