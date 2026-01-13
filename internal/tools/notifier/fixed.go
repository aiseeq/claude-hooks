package notifier

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/tools"
)

// NotifierTool notification tool for Stop hook
type NotifierTool struct {
	*tools.BaseTool
	kdeOnly       bool
	flashDuration int
	workDir       string // configurable work directory
}

// NewNotifierTool creates new notifier tool
func NewNotifierTool(config core.ToolConfig, logger core.Logger) (*NotifierTool, error) {
	supportedTools := []string{"Stop"}
	base := tools.NewBaseTool("notifier", config.Enabled, supportedTools, logger)

	// Get work directory from config or use HOME/work as default
	workDir := config.WorkDir
	if workDir == "" {
		home := os.Getenv("HOME")
		if home != "" {
			workDir = filepath.Join(home, "work")
		}
	}

	tool := &NotifierTool{
		BaseTool:      base,
		kdeOnly:       config.KDEOnly,
		flashDuration: config.FlashDuration,
		workDir:       workDir,
	}

	return tool, nil
}

// ValidateTool processes Stop hook
func (t *NotifierTool) ValidateTool(ctx context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	if input.ToolName != "Stop" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	t.Logger().Debug("Stop event detected - sending notifications")

	projectName := t.extractProjectName(input.TranscriptPath)
	t.Logger().Debug("extracted project name", "project", projectName, "path", input.TranscriptPath)

	// Console urgency hint
	consoleTitle := fmt.Sprintf("Claude Code (%s) - ready", projectName)
	t.setConsoleTitle(consoleTitle)

	// Terminal title
	terminalTitle := fmt.Sprintf("🔔 Claude Code [%s] - READY", projectName)
	t.setTerminalTitle(terminalTitle)

	// Play sound and send notification (with WaitGroup for sync)
	var wg sync.WaitGroup
	t.playWindowAttentionSound(&wg)

	notificationTitle := "Claude Code session completed"
	notificationMessage := fmt.Sprintf("Project: %s", projectName)
	t.sendDesktopNotification(notificationTitle, notificationMessage, &wg)

	wg.Wait()

	t.Logger().Debug("notifications sent (sound + terminal title)")

	notification := core.Violation{
		Type:       "notification_sent",
		Message:    fmt.Sprintf("Claude Code session [%s] completed - notifications sent", projectName),
		Suggestion: fmt.Sprintf("Notifications activated for project [%s]", projectName),
		Severity:   core.LevelInfo,
		Line:       0,
		Column:     0,
	}

	return &core.ValidationResult{
		IsValid:     true,
		Violations:  []core.Violation{notification},
		Suggestions: []string{fmt.Sprintf("Notifications sent for project [%s]", projectName)},
	}, nil
}

// extractProjectName extracts project name from transcript path
func (t *NotifierTool) extractProjectName(transcriptPath string) string {
	t.Logger().Debug("extracting project name", "transcript_path", transcriptPath)

	if transcriptPath == "" {
		if wd, err := os.Getwd(); err == nil {
			t.Logger().Debug("using working directory", "wd", wd)
			return t.extractProjectFromPath(wd)
		}
		return "unknown"
	}

	return t.extractProjectFromPath(transcriptPath)
}

// extractProjectFromPath extracts project name from any path
func (t *NotifierTool) extractProjectFromPath(path string) string {
	// Build dynamic regex patterns based on workDir
	if t.workDir != "" {
		// Escaped path for regex (replace / with escaped version)
		escapedWorkDir := regexp.QuoteMeta(t.workDir)

		// Pattern for direct path: /home/user/work/PROJECT/ or /home/user/work/PROJECT
		directPattern := regexp.MustCompile(escapedWorkDir + `/([^/]+)(?:/|$)`)
		if matches := directPattern.FindStringSubmatch(path); len(matches) > 1 {
			t.Logger().Debug("matched direct work pattern", "project", matches[1])
			return matches[1]
		}

		// Pattern for encoded path in transcript: -home-user-work-PROJECT/
		encodedWorkDir := regexp.QuoteMeta(pathToEncoded(t.workDir))
		encodedPattern := regexp.MustCompile(encodedWorkDir + `-([^/]+)/`)
		if matches := encodedPattern.FindStringSubmatch(path); len(matches) > 1 {
			t.Logger().Debug("matched encoded work pattern", "project", matches[1])
			return matches[1]
		}

		// Check for saga-agents subdirectory
		agentsPath := filepath.Join(t.workDir, "saga-agents")
		escapedAgents := regexp.QuoteMeta(agentsPath)
		agentsPattern := regexp.MustCompile(escapedAgents + `/([^/]+)(?:/|$)`)
		if matches := agentsPattern.FindStringSubmatch(path); len(matches) > 1 {
			t.Logger().Debug("matched agents pattern", "project", matches[1])
			return matches[1]
		}
	}

	t.Logger().Debug("no pattern matched, returning unknown", "path", path)
	return "unknown"
}

// pathToEncoded converts path to encoded format (/ -> -)
func pathToEncoded(path string) string {
	// Remove leading slash and replace all / with -
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}
	result := ""
	for _, c := range path {
		if c == '/' {
			result += "-"
		} else {
			result += string(c)
		}
	}
	return result
}

// setConsoleTitle sets console title
func (t *NotifierTool) setConsoleTitle(title string) {
	fmt.Fprintf(os.Stderr, "\033]30;%s\007", title)
	t.Logger().Debug("console title set", "title", title)
}

// setTerminalTitle sets terminal title
func (t *NotifierTool) setTerminalTitle(title string) {
	fmt.Fprintf(os.Stderr, "\033]0;%s\007", title)
	t.Logger().Debug("terminal title set", "title", title)
}

// playWindowAttentionSound plays window-attention sound
// wg can be nil for fire-and-forget mode
func (t *NotifierTool) playWindowAttentionSound(wg *sync.WaitGroup) {
	// Priority 1: canberra-gtk-play
	if t.tryPlaySound(wg, "canberra-gtk-play", "-i", "window-attention") {
		t.Logger().Debug("window-attention sound played via canberra-gtk-play")
		return
	}

	// Priority 2: paplay with window-attention.oga
	soundPath := "/usr/share/sounds/freedesktop/stereo/window-attention.oga"
	if _, err := os.Stat(soundPath); err == nil {
		if t.tryPlaySound(wg, "paplay", soundPath) {
			t.Logger().Debug("window-attention sound played via paplay (oga)")
			return
		}
	}

	// Priority 3: paplay with Front_Left.wav
	altSoundPath := "/usr/share/sounds/alsa/Front_Left.wav"
	if _, err := os.Stat(altSoundPath); err == nil {
		if t.tryPlaySound(wg, "paplay", altSoundPath) {
			t.Logger().Debug("alternative sound played via paplay (wav)")
			return
		}
	}

	t.Logger().Debug("no sound system available")
}

// tryPlaySound attempts to play sound with given command
func (t *NotifierTool) tryPlaySound(wg *sync.WaitGroup, command string, args ...string) bool {
	if _, err := exec.LookPath(command); err != nil {
		return false
	}

	if wg != nil {
		wg.Add(1)
	}

	go func() {
		if wg != nil {
			defer wg.Done()
		}
		cmd := exec.CommandContext(context.Background(), command, args...)
		if err := cmd.Run(); err != nil {
			t.Logger().Debug("sound command failed", "command", command, "error", err)
		} else {
			t.Logger().Debug("sound command successful", "command", command)
		}
	}()

	return true
}

// sendDesktopNotification sends desktop notification
// wg can be nil for fire-and-forget mode
func (t *NotifierTool) sendDesktopNotification(title, message string, wg *sync.WaitGroup) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		t.Logger().Debug("notify-send not available")
		return
	}

	if wg != nil {
		wg.Add(1)
	}

	go func() {
		if wg != nil {
			defer wg.Done()
		}
		cmd := exec.CommandContext(context.Background(), "notify-send",
			title, message,
			"--urgency=low",
			"--expire-time=5000")

		if err := cmd.Run(); err != nil {
			t.Logger().Debug("notify-send command failed", "error", err, "title", title, "message", message)
		} else {
			t.Logger().Debug("notify-send command successful", "title", title, "message", message)
		}
	}()

	t.Logger().Debug("desktop notification sent", "title", title, "message", message)
}
