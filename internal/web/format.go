package web

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/utibeabasi6/ukc-runner-controller/internal/controller"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func clock(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func duration(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func size(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return strconv.Itoa(mb/1024) + " GiB"
	}
	return strconv.Itoa(mb) + " MiB"
}

func shape(vcpus, memoryMB, diskMB int) string {
	cpu := "vCPU"
	if vcpus != 1 {
		cpu = "vCPUs"
	}
	return fmt.Sprintf("%d %s · %s · %s disk", vcpus, cpu, size(memoryMB), size(diskMB))
}

// workflowFile turns a job workflow ref such as
// "acme/app/.github/workflows/ci.yml@refs/heads/main" into "ci.yml".
func workflowFile(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	return ref[strings.LastIndex(ref, "/")+1:]
}

func jobTone(status string) string {
	switch status {
	case store.JobRunning:
		return "tint"
	case store.JobSucceeded:
		return "ok"
	case store.JobFailed:
		return "danger"
	default:
		return ""
	}
}

func runnerTone(state store.RunnerState) string {
	switch state {
	case store.RunnerBusy:
		return "tint"
	case store.RunnerFinished:
		return "ok"
	case store.RunnerFailed:
		return "danger"
	default:
		return ""
	}
}

func listenerTone(state string) string {
	switch state {
	case controller.StateListening:
		return "ok"
	case controller.StateError:
		return "danger"
	default:
		return ""
	}
}

func pct(used, total int64) string {
	if total <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d%%", used*100/total)
}

func jobName(j store.Job) string {
	if j.DisplayName != "" {
		return j.DisplayName
	}
	return "Job " + j.JobID[:min(8, len(j.JobID))]
}

func jobURL(j store.Job) templ.SafeURL {
	return templ.SafeURL("/jobs/" + url.PathEscape(j.JobID))
}
