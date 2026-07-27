package notifier

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/777genius/claude-notifications/internal/daemon"
)

// Explicit values are honoured verbatim, without consulting the installed
// zellij — that is the whole point of being able to force the legacy path.
func TestResolveZellijFocusMode_ExplicitValues(t *testing.T) {
	cases := []struct {
		configured string
		want       string
	}{
		{"pane", daemon.ZellijFocusModePane},
		{"tab", daemon.ZellijFocusModeTab},
		{"off", daemon.ZellijFocusModeOff},
		{"  TAB  ", daemon.ZellijFocusModeTab},
		{"Off", daemon.ZellijFocusModeOff},
	}

	for _, testCase := range cases {
		t.Run(testCase.configured, func(t *testing.T) {
			if got := resolveZellijFocusMode(testCase.configured); got != testCase.want {
				t.Errorf("resolveZellijFocusMode(%q) = %q, want %q", testCase.configured, got, testCase.want)
			}
		})
	}
}

// Unset, "auto" and unrecognised values all resolve by capability: pane wins
// only when the session exports a pane ID and the installed zellij accepts
// focus-pane-id.
func TestResolveZellijFocusMode_AutoPicksPaneWhenTargetable(t *testing.T) {
	stubZellij(t, 2, "focus-pane-id")
	t.Setenv("ZELLIJ_SESSION_NAME", "cubic-weasel")
	t.Setenv("ZELLIJ_PANE_ID", "2")

	for _, configured := range []string{"", "auto", "AUTO", "nonsense"} {
		t.Run(configured, func(t *testing.T) {
			if got := resolveZellijFocusMode(configured); got != daemon.ZellijFocusModePane {
				t.Errorf("resolveZellijFocusMode(%q) = %q, want %q", configured, got, daemon.ZellijFocusModePane)
			}
		})
	}
}

func TestResolveZellijFocusMode_AutoFallsBackToTab(t *testing.T) {
	t.Run("zellij predating focus-pane-id", func(t *testing.T) {
		stubZellij(t, 2, "go-to-tab-name")
		t.Setenv("ZELLIJ_SESSION_NAME", "cubic-weasel")
		t.Setenv("ZELLIJ_PANE_ID", "2")

		if got := resolveZellijFocusMode("auto"); got != daemon.ZellijFocusModeTab {
			t.Errorf("resolveZellijFocusMode(auto) = %q, want %q", got, daemon.ZellijFocusModeTab)
		}
	})

	// A session recognised by the $ZELLIJ marker alone has no pane for
	// focus-pane-id to name, however capable the binary is.
	t.Run("session exporting no pane ID", func(t *testing.T) {
		stubZellij(t, 2, "focus-pane-id")
		t.Setenv("ZELLIJ", "0")
		t.Setenv("ZELLIJ_SESSION_NAME", "cubic-weasel")
		t.Setenv("ZELLIJ_PANE_ID", "")

		if got := resolveZellijFocusMode("auto"); got != daemon.ZellijFocusModeTab {
			t.Errorf("resolveZellijFocusMode(auto) = %q, want %q", got, daemon.ZellijFocusModeTab)
		}
	})

	// The mirror image: $ZELLIJ alone makes the session recognisable, so the
	// hints can name a pane with no session to run the action against.
	t.Run("session exporting no session name", func(t *testing.T) {
		stubZellij(t, 2, "focus-pane-id")
		t.Setenv("ZELLIJ", "0")
		t.Setenv("ZELLIJ_SESSION_NAME", "")
		t.Setenv("ZELLIJ_PANE_ID", "2")

		if got := resolveZellijFocusMode("auto"); got != daemon.ZellijFocusModeTab {
			t.Errorf("resolveZellijFocusMode(auto) = %q, want %q", got, daemon.ZellijFocusModeTab)
		}
	})
}

// A tab name is free-form — `zellij action rename-tab "it's here"` is accepted —
// and -execute is handed to a shell, so a quote in one must not end up steering
// the command.
func TestBuildZellijActionNotifierArgs_QuotesTheTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the split is done by /bin/sh, and -execute is macOS-only anyway")
	}

	args := buildZellijActionNotifierArgs(
		"Title", "Body",
		"o'brien",
		"org.alacritty",
		"go-to-tab-name",
		"it's here'; echo escaped-the-quotes; echo '",
	)

	execute := ""
	for index, arg := range args {
		if arg == "-execute" && index+1 < len(args) {
			execute = args[index+1]
		}
	}
	if execute == "" {
		t.Fatalf("buildZellijActionNotifierArgs() produced no -execute argument: %v", args)
	}

	// Splitting the way a shell does is the only check that means anything here:
	// the target has to survive as exactly one word, whatever it contains. The
	// payload only echoes if the quoting ever breaks, so a regression reports
	// itself instead of running something.
	fields, err := shellFields(execute)
	if err != nil {
		t.Fatalf("failed to split %q the way a shell would: %v", execute, err)
	}

	want := []string{getZellijPath(), "-s", "o'brien", "action", "go-to-tab-name", "it's here'; echo escaped-the-quotes; echo '"}
	if len(fields) != len(want) {
		t.Fatalf("-execute split into %d words %q, want %d %q", len(fields), fields, len(want), want)
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Errorf("word %d = %q, want %q", i, fields[i], want[i])
		}
	}
}

// shellFields asks /bin/sh itself how the command would be split, rather than
// reimplementing its quoting rules in the test that checks them.
func shellFields(command string) ([]string, error) {
	// printf %s\0 keeps words that contain spaces or newlines intact.
	out, err := exec.Command("/bin/sh", "-c", "printf '%s\\0' "+command).Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), nil
}

// Both strategies must name the same session explicitly and invoke the action
// the platform's zellij actually supports.
func TestBuildZellijPaneNotifierArgs_ExecutesFocusPaneID(t *testing.T) {
	args := buildZellijPaneNotifierArgs("Title", "Body", "2", "cubic-weasel", "org.alacritty")

	execute := ""
	for index, arg := range args {
		if arg == "-execute" && index+1 < len(args) {
			execute = args[index+1]
		}
	}

	if execute == "" {
		t.Fatalf("buildZellijPaneNotifierArgs() produced no -execute argument: %v", args)
	}
	for _, want := range []string{"focus-pane-id", "'2'", "-s 'cubic-weasel'"} {
		if !strings.Contains(execute, want) {
			t.Errorf("-execute = %q, want it to contain %q", execute, want)
		}
	}
	if strings.Contains(execute, "go-to-tab-name") {
		t.Errorf("-execute = %q, should not fall back to the tab action", execute)
	}
}
