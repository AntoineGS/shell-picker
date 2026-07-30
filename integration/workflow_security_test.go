package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestWorkflowDispatchInputCannotBecomeShellSourceOrExecute(t *testing.T) {
	workflow := readWorkflow(t, "real-fzf.yml")
	step := workflowStep(t, workflow, "Install official fzf archive")
	script := workflowRunScript(t, step)
	if strings.Contains(script, "${{") {
		t.Fatal("GitHub expression is interpolated into shell source")
	}
	if !strings.Contains(step, "FZF_VERSION: ${{ inputs.fzf_version || '0.74.1' }}") {
		t.Fatal("workflow dispatch input is not passed through the step environment")
	}

	payloads := []string{
		`0.74.1'"; touch "$WORKFLOW_MARKER"; : '`,
		"0.74.1\n$(touch \"$WORKFLOW_MARKER\")",
		"0.74.1`touch \"$WORKFLOW_MARKER\"`",
	}
	for _, payload := range payloads {
		t.Run(strings.NewReplacer("\n", " newline ", "/", " slash ").Replace(payload), func(t *testing.T) {
			directory := t.TempDir()
			marker := filepath.Join(directory, "executed")
			mkdir := filepath.Join(directory, "mkdir")
			if err := os.WriteFile(mkdir, []byte("#!/bin/sh\n: > \"$WORKFLOW_MARKER\"\nexit 99\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("bash", "-c", script)
			command.Env = []string{"FZF_VERSION=" + payload, "RUNNER_OS=Linux", "WORKFLOW_MARKER=" + marker, "PATH=" + directory}
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("adversarial version succeeded: %s", output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("adversarial version reached an external command: %v", err)
			}
		})
	}
}

func TestReleaseWorkflowUsesPublishOnlyWritePermission(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	top, jobs := workflowJobSections(t, workflow)
	if got := workflowPermissions(top); len(got) != 1 || got["contents"] != "read" {
		t.Fatalf("top-level permissions=%v, want exactly contents: read", got)
	}
	for name, section := range jobs {
		permissions := workflowPermissions(section)
		if name == "publish" {
			if len(permissions) != 1 || permissions["contents"] != "write" {
				t.Errorf("publish permissions=%v, want exactly contents: write", permissions)
			}
			if !strings.Contains(section, "GH_TOKEN: ${{ github.token }}") {
				t.Error("publish job does not authenticate gh through GH_TOKEN")
			}
		} else if permissions["contents"] == "write" || strings.Contains(section, "GH_TOKEN:") {
			t.Errorf("job %s has publication credentials or write permission", name)
		}
		checkoutCount := strings.Count(section, "uses: actions/checkout@")
		if checkoutCount != strings.Count(section, "persist-credentials: false") {
			t.Errorf("job %s has %d checkouts but does not disable credentials for each", name, checkoutCount)
		}
	}
	if len(jobs) != 2 {
		t.Fatalf("release jobs=%v, want build and publish", sortedKeys(jobs))
	}
}

func workflowStep(t *testing.T, workflow, name string) string {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	start := -1
	needle := "      - name: " + name
	for index, line := range lines {
		if line == needle {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatalf("workflow has no step %q", name)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if strings.HasPrefix(lines[index], "      - ") {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func workflowRunScript(t *testing.T, step string) string {
	t.Helper()
	lines := strings.Split(step, "\n")
	for index, line := range lines {
		if line != "        run: |" {
			continue
		}
		var script []string
		for _, source := range lines[index+1:] {
			if !strings.HasPrefix(source, "          ") {
				break
			}
			script = append(script, strings.TrimPrefix(source, "          "))
		}
		return strings.Join(script, "\n")
	}
	t.Fatal("workflow step has no literal run block")
	return ""
}

func workflowJobSections(t *testing.T, workflow string) (string, map[string]string) {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	jobsLine := -1
	for index, line := range lines {
		if line == "jobs:" {
			jobsLine = index
			break
		}
	}
	if jobsLine < 0 {
		t.Fatal("workflow has no jobs mapping")
	}
	jobHeader := regexp.MustCompile(`^  ([A-Za-z0-9_-]+):$`)
	sections := map[string]string{}
	name, start := "", -1
	for index := jobsLine + 1; index <= len(lines); index++ {
		var match []string
		if index < len(lines) {
			match = jobHeader.FindStringSubmatch(lines[index])
		}
		if len(match) == 0 && index < len(lines) {
			continue
		}
		if name != "" {
			sections[name] = strings.Join(lines[start:index], "\n")
		}
		if index < len(lines) {
			name, start = match[1], index
		}
	}
	return strings.Join(lines[:jobsLine], "\n"), sections
}

func workflowPermissions(section string) map[string]string {
	lines := strings.Split(section, "\n")
	permissions := map[string]string{}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "permissions: {contents: read}" {
			permissions["contents"] = "read"
			continue
		}
		if trimmed != "permissions:" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		for _, item := range lines[index+1:] {
			itemIndent := len(item) - len(strings.TrimLeft(item, " "))
			if strings.TrimSpace(item) == "" {
				continue
			}
			if itemIndent <= indent {
				break
			}
			key, value, ok := strings.Cut(strings.TrimSpace(item), ":")
			if ok {
				permissions[key] = strings.TrimSpace(value)
			}
		}
	}
	return permissions
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
