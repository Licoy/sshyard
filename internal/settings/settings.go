// Package settings keeps SSHYard's preferences in a JSON file.
package settings

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/highlight"
	"github.com/Licoy/sshyard/internal/keymap"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
)

// Settings are the user's preferences.
type Settings struct {
	Background Background `json:"background"`
	Theme      theme.Mode `json:"theme"`
	// Language is "" to follow the system, else one of Languages.
	Language string `json:"language"`
	// CloseBehavior controls whether closing the main window hides it or quits.
	CloseBehavior string `json:"closeBehavior"`
	// RememberLayout restores window geometry and the workspace on next launch.
	RememberLayout bool `json:"rememberLayout"`
	// ToolPanes is how the right rail opens its tools: "", the default,
	// in the place of the one open in the tab, one at a time, or
	// ToolPanesSplit beside the ones already open.
	ToolPanes string `json:"toolPanes,omitempty"`
	// TrayIcon selects the monochrome or colorful application logo.
	TrayIcon string `json:"trayIcon"`
	// TrayClick selects the popup or the main window on a tray click.
	TrayClick string `json:"trayClick"`
	// TrayMaterial is independent of the main window material.
	TrayMaterial string `json:"trayMaterial"`
	// TrayHeight remembers the popup height chosen by dragging.
	TrayHeight   int  `json:"trayHeight"`
	CheckUpdates bool `json:"checkUpdates"`
	// UpdateIntervalHours is the time between automatic checks for
	// updates, one of UpdateIntervals. The updater reads it at launch.
	UpdateIntervalHours int     `json:"updateIntervalHours"`
	TerminalFontSize    int     `json:"terminalFontSize"`
	KeepAliveSeconds    int     `json:"keepAliveSeconds"`
	AutoReconnect       bool    `json:"autoReconnect"`
	SidebarWidth        float32 `json:"sidebarWidth"`
	// TerminalType is the TERM new terminals report, unless their host
	// names its own (Host.Term); "" is session.DefaultTerm.
	TerminalType string `json:"terminalType,omitempty"`
	// UpdateProxy is how the checks for updates and their downloads go
	// out: "" the system's proxy settings, ProxyDirect none, else a
	// proxy's ID.
	UpdateProxy string `json:"updateProxy,omitempty"`
	// TerminalLineHeight and TerminalCharWidth are the height of the
	// terminal's rows and the width of its cells, in percent of the
	// font's own line and advance.
	TerminalLineHeight int `json:"terminalLineHeight"`
	TerminalCharWidth  int `json:"terminalCharWidth"`
	// SidebarHidden hides the hosts' sidebar beside the workspace.
	SidebarHidden bool `json:"sidebarHidden,omitempty"`
	// Privacy masks the IP addresses the app shows, for a screen share
	// or a recording. What is typed in a form is shown as it is.
	Privacy bool `json:"privacy,omitempty"`
	// ChromeMaterial is how the sidebar and the title bar show the
	// window's material (macOS): MaterialTranslucent, the default, or
	// MaterialOpaque. ContentMaterial is that of the content beside them,
	// opaque by default.
	ChromeMaterial  string `json:"chromeMaterial,omitempty"`
	ContentMaterial string `json:"contentMaterial,omitempty"`
	// UIZoom is how large the interface shows, in percent, one of
	// UIZooms; 0, the default, follows the system's text size.
	UIZoom int `json:"uiZoom,omitempty"`
	// UIFont is the interface's font family, "" for the system's.
	UIFont string `json:"uiFont,omitempty"`
	// TerminalCursorStyle selects the terminal cursor shape.
	TerminalCursorStyle string `json:"terminalCursorStyle"`
	// TerminalCursorBlink allows the terminal cursor to blink.
	TerminalCursorBlink bool `json:"terminalCursorBlink"`
	// TerminalLineNumbers and TerminalTimestamps show, left of the
	// terminal's lines, their number and the time output reached them;
	// both off by default.
	TerminalLineNumbers bool `json:"terminalLineNumbers,omitempty"`
	TerminalTimestamps  bool `json:"terminalTimestamps,omitempty"`
	// TerminalFont is the terminal's font family, "" for the system's
	// monospaced font.
	TerminalFont string `json:"terminalFont,omitempty"`
	// TerminalFallbackFont is the family the terminal draws what
	// TerminalFont lacks (CJK and other non-ASCII text) with, before the
	// system's monospaced font; "" for none.
	TerminalFallbackFont string `json:"terminalFallbackFont,omitempty"`
	// TerminalScheme is the terminal's color scheme, the ID of a
	// built-in (theme.Schemes) or imported one; "" the app's own colors
	// for the appearance.
	TerminalScheme string `json:"terminalScheme,omitempty"`
	// TerminalSchemes are the color schemes the user imported.
	TerminalSchemes []theme.Scheme `json:"terminalSchemes,omitempty"`
	// Highlight is how the terminal colors keywords in its output.
	Highlight Highlight `json:"highlight"`
	// LocalShell is the ID of the shell local terminals open by default
	// (session.Shell); "" or one no longer installed is the first found.
	LocalShell string `json:"localShell,omitempty"`
	// OpenLocalOnStart opens a local terminal when the app starts.
	OpenLocalOnStart bool `json:"openLocalOnStart,omitempty"`
	// Shortcuts are the shortcuts the user recorded, by the ID of the
	// app's action, as keymap writes them; the others keep their default.
	Shortcuts map[string]string `json:"shortcuts,omitempty"`
	// MonospacedOnly limits the terminal font choice to monospaced
	// families.
	MonospacedOnly bool `json:"monospacedOnly"`
	// TransferConcurrency is how many SFTP transfers of a files pane run
	// at once, 1 to 10.
	TransferConcurrency int `json:"transferConcurrency"`
	// OpenConfirmMB is the size, in MB, from which opening a remote file
	// (in the editor or a local program) asks first, 1 to 1024.
	OpenConfirmMB int `json:"openConfirmMB"`
	// FilesDual shows this computer's files beside the server's in a
	// files pane; off, the default, shows the server's alone. A new pane
	// follows the last choice.
	FilesDual bool `json:"filesDual,omitempty"`
	// FilesSplit is the share, in percent, of the first side of a files
	// pane showing both, 10 to 90.
	FilesSplit float32 `json:"filesSplit,omitempty"`
	// FilesEdge is where a files pane opened from a terminal goes:
	// FilesRight, the default, or FilesBelow.
	FilesEdge string `json:"filesEdge,omitempty"`
	// FilesZoom is how large the rows of files panes show, in percent,
	// one of FilesZooms; 0 is 100.
	FilesZoom int `json:"filesZoom,omitempty"`
	// EditorTreeWidth is the width, in DIPs, of the folders beside the
	// text in an editor window, 140 to 600; EditorTreeHidden hides them.
	EditorTreeWidth  float32 `json:"editorTreeWidth,omitempty"`
	EditorTreeHidden bool    `json:"editorTreeHidden,omitempty"`
	// TerminalRightClick is what a right click in a terminal does:
	// RightClickMenu (the default), RightClickPaste or
	// RightClickCopyPaste. A program reading the mouse takes it first,
	// and Shift+right-click always opens the menu.
	TerminalRightClick string `json:"terminalRightClick,omitempty"`
	// CopyOnSelect copies text to the clipboard as it is selected in a
	// terminal.
	CopyOnSelect bool `json:"copyOnSelect,omitempty"`
	// OSC52Write is whether a remote program may copy to the local
	// clipboard: allow, ask (the default) or deny.
	OSC52Write string `json:"osc52Write"`
	// OSC52MaxBytes is the largest decoded OSC 52 paste to keep,
	// from 64 KiB to 8 MiB.
	OSC52MaxBytes int `json:"osc52MaxBytes"`
	// OSC52Notify shows a notice, with Undo, after a remote program
	// writes to the clipboard. Off writes silently.
	OSC52Notify bool `json:"osc52Notify"`
	// MetricsEnabled shows the terminal metrics bar. Off hides it on every
	// host. A host can still hide its own bar while this is on.
	MetricsEnabled bool `json:"metricsEnabled"`
	// MetricsInterval is how often a host is sampled, in seconds: 1, 2, 5 or 10.
	MetricsInterval int `json:"metricsInterval"`
	// MetricsMount is an extra filesystem shown beside the root. Empty is the root only.
	MetricsMount string `json:"metricsMount,omitempty"`
	// AIDefaultAgent is the agent a new AI pane starts with. "builtin"
	// is the in-process agent, which speaks to the configured providers;
	// "acp:<id>" is one of AIACPAgents, and "acp" alone the first of them.
	AIDefaultAgent string `json:"aiDefaultAgent,omitempty"`
	// AIACPAgents are the external ACP agents: ones detected on this
	// computer, or user-supplied executables; arguments are passed
	// directly, without a shell.
	AIACPAgents []ACPConfig `json:"aiACPAgents,omitempty"`
	// AIACP is the single agent older files kept; Normalize moves it into
	// AIACPAgents.
	AIACP *ACPConfig `json:"aiACP,omitempty"`
	// AIDefaultMode is the global permission mode: ask, review or full.
	// "full" is kept when a file already says so; the UI asks before
	// writing it.
	AIDefaultMode string `json:"aiDefaultMode,omitempty"`
	// AIReviewProvider is an AIProviders ID, or empty for no review model.
	AIReviewProvider string `json:"aiReviewProvider,omitempty"`
	// AIReviewKind is how the review model is asked: AIReviewChat,
	// AIReviewDecisions or AIReviewJev, as the provider's format allows.
	AIReviewKind string `json:"aiReviewKind,omitempty"`
	// AIReviewModel is the model reviewing. Empty is the provider's
	// default for AIReviewChat, and the kind's default model otherwise.
	AIReviewModel string `json:"aiReviewModel,omitempty"`
	// AIReviewStrictness is how sure a decision model must be that an
	// action is safe to allow it: loose, standard or strict.
	AIReviewStrictness string `json:"aiReviewStrictness,omitempty"`
	// AIDefaultProvider is the AIProviders ID a new AI pane asks, or empty
	// for the first of them.
	AIDefaultProvider string `json:"aiDefaultProvider,omitempty"`
	// AIContextLines is how many lines of the linked terminal are sent
	// with a question, 50 to 2000.
	AIContextLines int `json:"aiContextLines,omitempty"`
	// AIAuditRetentionDays is how long audit files are kept.
	AIAuditRetentionDays int `json:"aiAuditRetentionDays,omitempty"`
	// AIRequestTimeout is how many seconds the built-in agent waits for a
	// reply to start, and then between two pieces of it, before the turn
	// fails, AIRequestTimeoutMin to AIRequestTimeoutMax.
	AIRequestTimeout int `json:"aiRequestTimeout,omitempty"`
	// AIMaxToolCalls caps the tool calls one AI turn runs before it pauses
	// for the user. Unset takes DefaultMaxToolCalls; a pointer to 0 runs
	// without a limit.
	AIMaxToolCalls *int `json:"aiMaxToolCalls,omitempty"`
	// AIRedact masks suspected private keys and tokens before they are
	// sent to a model. The default is on; false is stored so it survives.
	AIRedact bool `json:"aiRedact"`
	// AIProviders are built-in agent profiles. API keys are not here;
	// they live in the keychain.
	AIProviders []AIProvider `json:"aiProviders,omitempty"`
	// AIConsentHosts are host IDs whose first-use AI notice the user
	// already confirmed. Nothing is sent for a host that is not listed.
	AIConsentHosts []string `json:"aiConsentHosts,omitempty"`
	// MCPEnabled serves the MCP endpoint on 127.0.0.1:MCPPort, for AI
	// agents on this computer. Off by default; the UI shows the risks
	// before turning it on. Its bearer token lives in the keychain.
	MCPEnabled bool `json:"mcpEnabled,omitempty"`
	// MCPPort is the endpoint's port, 1024 to 65535.
	MCPPort int `json:"mcpPort,omitempty"`
	// LocalKeys lets a host that asks for its password first try the SSH
	// agent's keys and the key files of this computer. On by default.
	LocalKeys bool `json:"localKeys"`
	// LocalKeysMCP lets MCP connections try them too, without asking
	// anything. Off by default; it needs LocalKeys.
	LocalKeysMCP bool `json:"localKeysMcp,omitempty"`
	// BackupTargets are the places this device backs up to. They belong
	// to this device: a backup neither holds nor replaces them.
	BackupTargets []BackupTarget `json:"backupTargets,omitempty"`
	// DefaultBackupPassword says a default backup password is set: the key
	// derived from it is in the keychain (keychain.DefaultBackupKey). It
	// belongs to this device too.
	DefaultBackupPassword bool `json:"defaultBackupPassword,omitempty"`
	// RenewalNotify shows a system notification as the renewal of a
	// host's subscription comes within its reminder. On by default.
	RenewalNotify bool `json:"renewalNotify"`
	// RenewalNotified is, by host ID, the renewal day (assets.DateLayout)
	// this device last notified of, so that each renewal notifies once.
	RenewalNotified map[string]string `json:"renewalNotified,omitempty"`
	// SessionLog is where the logs of terminal sessions go. It belongs to
	// this device.
	SessionLog SessionLog `json:"sessionLog"`
}

// SessionLog is how terminal sessions are logged.
type SessionLog struct {
	// Dir is the folder of the logs, an absolute path; "" is
	// SSHYard/logs in the home folder.
	Dir string `json:"dir,omitempty"`
	// Raw keeps the bytes printed, escape sequences and all, rather than
	// the lines shown; Stamps starts each line with its time.
	Raw    bool `json:"raw,omitempty"`
	Stamps bool `json:"stamps,omitempty"`
}

// Highlight is how the terminal colors keywords in its output.
type Highlight struct {
	// On colors them at all.
	On bool `json:"on"`
	// PresetsOff are the IDs of the built-in rules (highlight.Presets)
	// turned off; the others are on.
	PresetsOff []string `json:"presetsOff,omitempty"`
	// Rules are the user's own, matched before the built-in ones.
	Rules []highlight.Rule `json:"rules,omitempty"`
	// Override colors the text programs colored too.
	Override bool `json:"override,omitempty"`
	// FullScreen colors full-screen programs (the alternate screen) too.
	FullScreen bool `json:"fullScreen,omitempty"`
}

// PresetsOn are the IDs of the built-in rules on.
func (h Highlight) PresetsOn() []string {
	var on []string
	for _, id := range highlight.PresetIDs() {
		if !slices.Contains(h.PresetsOff, id) {
			on = append(on, id)
		}
	}
	return on
}

// normalize keeps the presets that exist and the rules with a pattern,
// in new slices: the ones held may be shared with the settings in force.
func (h *Highlight) normalize() {
	var off []string
	for _, id := range highlight.PresetIDs() {
		if slices.Contains(h.PresetsOff, id) {
			off = append(off, id)
		}
	}
	h.PresetsOff = off
	var rules []highlight.Rule
	for _, r := range h.Rules {
		if r.Pattern == "" || len(r.Pattern) > highlight.MaxPattern || len(rules) == highlight.MaxRules {
			continue
		}
		if r.Color > 15 {
			r.Color = 1
		}
		rules = append(rules, r)
	}
	h.Rules = rules
}

// The kinds of BackupTarget.
const (
	BackupWebDAV = "webdav"
	BackupS3     = "s3"
	BackupGist   = "gist"
)

// BackupAutoHours are the times between automatic backups a target may
// have, in hours.
var BackupAutoHours = []int{6, 24, 168}

// BackupKeepDefault is how many of this device's backups a new target
// keeps; BackupKeepMax bounds it.
const (
	BackupKeepDefault = 20
	BackupKeepMax     = 1000
)

// BackupTarget is a place backups go: a folder on a WebDAV server, an S3
// bucket or a GitHub gist. Its password, secret key or token is in the
// keychain (keychain.BackupSecret), and so is the key its backups are
// sealed with, by hand or automatically (keychain.BackupKey).
type BackupTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// URL is the WebDAV server, or the S3 endpoint ("" for AWS).
	URL string `json:"url,omitempty"`
	// User is the WebDAV user, or the S3 access key ID.
	User   string `json:"user,omitempty"`
	Bucket string `json:"bucket,omitempty"`
	Region string `json:"region,omitempty"`
	// Dir is the folder on the WebDAV server, or the key prefix in the
	// bucket.
	Dir string `json:"dir,omitempty"`
	// PathStyle names the bucket in the path rather than the host name.
	PathStyle bool `json:"pathStyle,omitempty"`
	// GistID is the gist the backups are in, "" until one is found or made.
	GistID string `json:"gistId,omitempty"`
	// ProxyID is the proxy the place is reached through, "" for none.
	ProxyID string `json:"proxyId,omitempty"`
	// Keep is how many of this device's backups stay; older ones are
	// deleted after a backup. 0 keeps them all.
	Keep int `json:"keep"`
	// DefaultPassword seals its backups with the default backup password
	// (keychain.DefaultBackupKey) rather than its own.
	DefaultPassword bool `json:"defaultPassword,omitempty"`
	// OmitSecrets leaves the secrets out of its backups.
	OmitSecrets bool `json:"omitSecrets,omitempty"`
	// AutoHours is the time between automatic backups, one of
	// BackupAutoHours; 0 is off.
	AutoHours int `json:"autoHours,omitempty"`
}

// backupKindOK reports whether k is a kind of BackupTarget.
func backupKindOK(k string) bool { return k == BackupWebDAV || k == BackupS3 || k == BackupGist }

// backupIDOK reports whether id can name a target, and its keychain entries.
func backupIDOK(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// normalizeBackups drops targets of no kind or ID, or an ID twice, and
// bounds the rest.
func (s *Settings) normalizeBackups() {
	var out []BackupTarget
	seen := map[string]bool{}
	for _, t := range s.BackupTargets {
		t.ID, t.Name, t.Kind = strings.TrimSpace(t.ID), strings.TrimSpace(t.Name), strings.TrimSpace(t.Kind)
		if !backupIDOK(t.ID) || seen[t.ID] || !backupKindOK(t.Kind) {
			continue
		}
		seen[t.ID] = true
		t.URL, t.User, t.Bucket, t.Region = strings.TrimSpace(t.URL), strings.TrimSpace(t.User), strings.TrimSpace(t.Bucket), strings.TrimSpace(t.Region)
		t.Dir, t.GistID, t.ProxyID = strings.TrimSpace(t.Dir), strings.TrimSpace(t.GistID), strings.TrimSpace(t.ProxyID)
		t.Keep = min(max(t.Keep, 0), BackupKeepMax)
		if !slices.Contains(BackupAutoHours, t.AutoHours) {
			t.AutoHours = 0
		}
		out = append(out, t)
	}
	s.BackupTargets = out
}

// BackupTarget is the target of id, and whether there is one.
func (s Settings) BackupTarget(id string) (BackupTarget, bool) {
	for _, t := range s.BackupTargets {
		if t.ID == id {
			return t, true
		}
	}
	return BackupTarget{}, false
}

// ACPConfig describes one external ACP backend. ID names it among the
// others: the detected agent's, else "custom", "custom-2" and so on.
// Agent is the detected agent's catalog id (internal/ai/acp/detect), or
// empty for a command the user typed; a detected agent is found again
// when its executable moves. Options are the values last chosen for the
// agent's own settings (its model and thinking level), by setting id.
type ACPConfig struct {
	ID      string            `json:"id,omitempty"`
	Agent   string            `json:"agent,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Options map[string]string `json:"options,omitempty"`
}

// SameLaunch says whether two configurations start the same agent the
// same way: chosen option values are not part of it.
func (c ACPConfig) SameLaunch(o ACPConfig) bool {
	return c.ID == o.ID && c.Agent == o.Agent && c.Command == o.Command && slices.Equal(c.Args, o.Args)
}

// ACP is the agent a pane's backend names: "acp:<id>" is that agent,
// "acp" the first one.
func (s Settings) ACP(backend string) (ACPConfig, bool) {
	id, ok := strings.CutPrefix(backend, "acp:")
	if !ok && backend != "acp" {
		return ACPConfig{}, false
	}
	for _, c := range s.AIACPAgents {
		if !ok || c.ID == id {
			return c, true
		}
	}
	return ACPConfig{}, false
}

// IsACP says whether a pane's backend is an external ACP agent.
func IsACP(backend string) bool { return backend == "acp" || strings.HasPrefix(backend, "acp:") }

func (s *Settings) normalizeACP() {
	if s.AIACP != nil {
		if len(s.AIACPAgents) == 0 {
			s.AIACPAgents = []ACPConfig{*s.AIACP}
		}
		s.AIACP = nil
	}
	seen := map[string]bool{}
	out := s.AIACPAgents[:0:0]
	for _, c := range s.AIACPAgents {
		c.ID, c.Agent, c.Command = strings.TrimSpace(c.ID), strings.TrimSpace(c.Agent), strings.TrimSpace(c.Command)
		if c.Command == "" {
			continue
		}
		base := c.ID
		if base == "" {
			base = c.Agent
		}
		if base == "" {
			base = "custom"
		}
		c.ID = base
		for n := 2; seen[c.ID]; n++ {
			c.ID = base + "-" + strconv.Itoa(n)
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	s.AIACPAgents = out
}

// AIProvider is one built-in agent profile. Headers are non-secret;
// the API key is keychain.AIKey(ID). Model is the default model a pane
// asks; Models are the endpoint's chat models, as last fetched.
//
// A nil Temperature, TopP or MaxTokens is not sent, so the provider's
// default applies. LegacyMaxTokens sends a Chat Completions limit as
// max_tokens instead of max_completion_tokens.
type AIProvider struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Format          string            `json:"format,omitempty"`
	BaseURL         string            `json:"baseUrl"`
	Model           string            `json:"model"`
	Models          []string          `json:"models,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"topP,omitempty"`
	MaxTokens       *int              `json:"maxTokens,omitempty"`
	LegacyMaxTokens bool              `json:"legacyMaxTokens,omitempty"`
	// Proxy is how the requests go out: "" the system's proxy settings,
	// ProxyDirect none, else a proxy's ID.
	Proxy string `json:"proxy,omitempty"`
}

// ProxyDirect is the proxy choice of AIProvider.Proxy and UpdateProxy
// that goes out without one, ignoring the system's proxy settings.
const ProxyDirect = "direct"

// The wire formats of an AIProvider, as internal/ai/llm names them.
const (
	AIFormatChat      = "chat"
	AIFormatResponses = "responses"
	AIFormatAnthropic = "anthropic"
	// AIFormatTypeSafe decides and does not chat: it only reviews.
	AIFormatTypeSafe = "typesafe"
)

// CanChat reports whether a pane can ask the provider questions.
func (p AIProvider) CanChat() bool { return p.Format != AIFormatTypeSafe }

// The ways a review model is asked. AIReviewChat asks a chat model for a
// JSON verdict; AIReviewDecisions asks OpenAI Decisions and AIReviewJev
// asks TypeSafe's Jev how likely the action is safe.
const (
	AIReviewChat      = "chat"
	AIReviewDecisions = "decisions"
	AIReviewJev       = "jev"
)

// The default models of the decision reviews.
const (
	AIDecisionsModel = "gpt-6-luna"
	AIJevModel       = "jev-latest"
)

// ReviewKinds are the kinds of review a provider of format can give.
func ReviewKinds(format string) []string {
	switch format {
	case AIFormatTypeSafe:
		return []string{AIReviewJev}
	case AIFormatAnthropic:
		return []string{AIReviewChat}
	}
	return []string{AIReviewChat, AIReviewDecisions}
}

// The strictness of a decision review.
const (
	AIReviewLoose    = "loose"
	AIReviewStandard = "standard"
	AIReviewStrict   = "strict"
)

// ReviewThreshold is the least probability that an action is safe for a
// decision review of strictness s to allow it.
func ReviewThreshold(s string) float64 {
	switch s {
	case AIReviewLoose:
		return 0.8
	case AIReviewStrict:
		return 0.97
	}
	return 0.9
}

// AIMaxTokensMax bounds AIProvider.MaxTokens; AIModelsMax bounds Models.
const (
	AIMaxTokensMax = 1_000_000
	AIModelsMax    = 1000
)

// DefaultMaxToolCalls is how many tool calls one AI turn runs before it
// pauses when AIMaxToolCalls is unset; AIMaxToolCallsMax bounds the setting.
const (
	DefaultMaxToolCalls = 25
	AIMaxToolCallsMax   = 10_000
)

// The bounds of AIRequestTimeout, in seconds, and its default.
const (
	DefaultAIRequestTimeout = 120
	AIRequestTimeoutMin     = 10
	AIRequestTimeoutMax     = 1800
)

// MaxToolCalls is the cap on the tool calls one AI turn runs before it
// pauses: the setting, DefaultMaxToolCalls when unset, and 0 for no limit.
func (s Settings) MaxToolCalls() int {
	if s.AIMaxToolCalls == nil {
		return DefaultMaxToolCalls
	}
	return min(max(*s.AIMaxToolCalls, 0), AIMaxToolCallsMax)
}

const (
	// OSC52Allow lets a remote program copy to the clipboard.
	OSC52Allow = "allow"
	// OSC52Ask asks before each copy.
	OSC52Ask = "ask"
	// OSC52Deny ignores remote copies.
	OSC52Deny = "deny"
	// OSC52MaxDefault is 1 MiB. OSC52MaxMin and OSC52MaxMax bound the setting.
	OSC52MaxDefault = 1 << 20
	OSC52MaxMin     = 64 << 10
	OSC52MaxMax     = 8 << 20
)

// The materials of a part of the window: the system's translucent
// material behind it, or its own solid fill.
const (
	MaterialTranslucent = "translucent"
	MaterialOpaque      = "opaque"
)

func materialOK(s string) bool { return s == MaterialTranslucent || s == MaterialOpaque }

// What a right click in a terminal does: open its menu, paste, or copy
// the selection and paste without one.
const (
	RightClickMenu      = "menu"
	RightClickPaste     = "paste"
	RightClickCopyPaste = "copyPaste"
)

// The places a files pane opened from a terminal goes: beside it on the
// right, or under it.
const (
	FilesRight = "right"
	FilesBelow = "below"
)

// ToolPanesSplit opens a rail tool beside the ones already open in the
// tab, rather than in the place of the one it shows.
const ToolPanesSplit = "split"

// MCPPortDefault is the MCP endpoint's port unless the user picks one.
const MCPPortDefault = 47320

// UpdateIntervals are the hours between automatic checks for updates the
// user can choose.
var UpdateIntervals = []int{1, 2, 6, 12, 24}

// UIZooms are the interface zooms, in percent, the user can choose
// besides following the system, smallest first.
var UIZooms = []int{80, 90, 100, 110, 125, 150, 175, 200}

// FilesZooms are the zooms of files panes' rows, in percent, the user
// can choose, smallest first.
var FilesZooms = []int{80, 90, 100, 110, 125, 150, 175, 200}

// FilesScale is FilesZoom as a factor, 1 for 100%.
func (s Settings) FilesScale() float32 {
	if s.FilesZoom == 0 {
		return 1
	}
	return float32(s.FilesZoom) / 100
}

// Languages are the languages SSHYard ships, besides following the system.
var Languages = []string{"en", "zh-Hans", "zh-Hant", "ko", "ja"}

// Main-window close behaviors.
const (
	CloseToTray   = "tray"
	CloseQuit     = "quit"
	TrayIconMono  = "mono"
	TrayIconColor = "color"
)

// Tray click actions and persisted popup height limits.
const (
	TrayClickPanel    = "panel"
	TrayClickWindow   = "window"
	DefaultTrayHeight = 536
	MinTrayHeight     = 320
	MaxTrayHeight     = 1200
)

// ClampTrayHeight defaults missing or invalid heights and bounds a saved height.
func ClampTrayHeight(height int) int {
	if height <= 0 {
		return DefaultTrayHeight
	}
	return min(max(height, MinTrayHeight), MaxTrayHeight)
}

// The bounds of TerminalFontSize, in points, and of TerminalLineHeight
// and TerminalCharWidth, in percent.
const (
	MinFontSize, MaxFontSize     = 8, 32
	MinLineHeight, MaxLineHeight = 80, 200
	MinCharWidth, MaxCharWidth   = 80, 150
)

// Terminal cursor shapes.
const (
	CursorBlock     = "block"
	CursorBar       = "bar"
	CursorUnderline = "underline"
)

// Default returns the defaults.
func Default() Settings {
	return Settings{Background: DefaultBackground(), Theme: theme.Auto, CloseBehavior: CloseToTray, TrayIcon: TrayIconColor, TrayClick: TrayClickPanel, TrayMaterial: MaterialTranslucent, TrayHeight: DefaultTrayHeight, CheckUpdates: true, UpdateIntervalHours: 1, TerminalFontSize: 13, TerminalLineHeight: 100, TerminalCharWidth: 100, TerminalCursorStyle: CursorBlock, TerminalCursorBlink: true, KeepAliveSeconds: 30, AutoReconnect: true, SidebarWidth: 240, ChromeMaterial: MaterialTranslucent, ContentMaterial: MaterialOpaque, MonospacedOnly: true, TransferConcurrency: 3, OpenConfirmMB: 10, FilesSplit: 50, FilesEdge: FilesRight, TerminalRightClick: RightClickMenu, EditorTreeWidth: 240, OSC52Write: OSC52Ask, OSC52MaxBytes: OSC52MaxDefault, OSC52Notify: true, MetricsEnabled: true, MetricsInterval: 2,
		AIDefaultAgent: "builtin", AIDefaultMode: "ask", AIReviewStrictness: AIReviewStandard, AIContextLines: 200, AIAuditRetentionDays: 90, AIRequestTimeout: DefaultAIRequestTimeout, AIRedact: true, MCPPort: MCPPortDefault, LocalKeys: true, Highlight: Highlight{On: true}, RenewalNotify: true}
}

// ResolveOSC52Write is the write policy in effect. A host value of allow,
// ask or deny wins; otherwise the global policy is used. Anything else
// is ask.
func ResolveOSC52Write(global, host string) string {
	switch host {
	case OSC52Allow, OSC52Ask, OSC52Deny:
		return host
	}
	switch global {
	case OSC52Allow, OSC52Ask, OSC52Deny:
		return global
	}
	return OSC52Ask
}

// Normalize replaces invalid values with defaults.
func (s *Settings) Normalize() {
	s.Background.normalize()
	d := Default()
	if s.TrayIcon != TrayIconMono && s.TrayIcon != TrayIconColor {
		s.TrayIcon = d.TrayIcon
	}
	if s.TrayClick != TrayClickPanel && s.TrayClick != TrayClickWindow {
		s.TrayClick = d.TrayClick
	}
	if !materialOK(s.TrayMaterial) {
		s.TrayMaterial = d.TrayMaterial
	}
	s.TrayHeight = ClampTrayHeight(s.TrayHeight)
	if s.CloseBehavior != CloseToTray && s.CloseBehavior != CloseQuit {
		s.CloseBehavior = d.CloseBehavior
	}
	if !s.Theme.Valid() {
		s.Theme = d.Theme
	}
	ok := s.Language == ""
	for _, l := range Languages {
		ok = ok || s.Language == l
	}
	if !ok {
		s.Language = ""
	}
	if !slices.Contains(UpdateIntervals, s.UpdateIntervalHours) {
		s.UpdateIntervalHours = d.UpdateIntervalHours
	}
	s.UpdateProxy = strings.TrimSpace(s.UpdateProxy)
	if s.UIZoom != 0 && !slices.Contains(UIZooms, s.UIZoom) {
		s.UIZoom = d.UIZoom
	}
	switch s.TerminalCursorStyle {
	case CursorBlock, CursorBar, CursorUnderline:
	default:
		s.TerminalCursorStyle = d.TerminalCursorStyle
	}
	if s.TerminalFontSize < MinFontSize || s.TerminalFontSize > MaxFontSize {
		s.TerminalFontSize = d.TerminalFontSize
	}
	if s.TerminalLineHeight < MinLineHeight || s.TerminalLineHeight > MaxLineHeight {
		s.TerminalLineHeight = d.TerminalLineHeight
	}
	if s.TerminalCharWidth < MinCharWidth || s.TerminalCharWidth > MaxCharWidth {
		s.TerminalCharWidth = d.TerminalCharWidth
	}
	if s.KeepAliveSeconds < 0 || s.KeepAliveSeconds > 600 {
		s.KeepAliveSeconds = d.KeepAliveSeconds
	}
	if s.TerminalType = strings.TrimSpace(s.TerminalType); s.TerminalType == session.DefaultTerm || !session.TermOK(s.TerminalType) {
		s.TerminalType = ""
	}
	if !materialOK(s.ChromeMaterial) {
		s.ChromeMaterial = d.ChromeMaterial
	}
	if !materialOK(s.ContentMaterial) {
		s.ContentMaterial = d.ContentMaterial
	}
	s.UIFont, s.TerminalFont = fontName(s.UIFont), fontName(s.TerminalFont)
	s.TerminalFallbackFont = fontName(s.TerminalFallbackFont)
	if s.SidebarWidth < 160 || s.SidebarWidth > 480 {
		s.SidebarWidth = d.SidebarWidth
	}
	if s.TransferConcurrency < 1 || s.TransferConcurrency > 10 {
		s.TransferConcurrency = d.TransferConcurrency
	}
	if s.OpenConfirmMB < 1 || s.OpenConfirmMB > 1024 {
		s.OpenConfirmMB = d.OpenConfirmMB
	}
	if s.FilesSplit < 10 || s.FilesSplit > 90 {
		s.FilesSplit = d.FilesSplit
	}
	if s.ToolPanes != ToolPanesSplit {
		s.ToolPanes = ""
	}
	if s.FilesEdge != FilesBelow {
		s.FilesEdge = FilesRight
	}
	if s.FilesZoom == 100 || !slices.Contains(FilesZooms, s.FilesZoom) {
		s.FilesZoom = 0
	}
	if s.EditorTreeWidth < 140 || s.EditorTreeWidth > 600 {
		s.EditorTreeWidth = d.EditorTreeWidth
	}
	switch s.TerminalRightClick {
	case RightClickMenu, RightClickPaste, RightClickCopyPaste:
	default:
		s.TerminalRightClick = RightClickMenu
	}
	switch s.OSC52Write {
	case OSC52Allow, OSC52Ask, OSC52Deny:
	default:
		s.OSC52Write = OSC52Ask
	}
	if s.OSC52MaxBytes < OSC52MaxMin || s.OSC52MaxBytes > OSC52MaxMax {
		s.OSC52MaxBytes = OSC52MaxDefault
	}
	s.MetricsInterval = session.MetricsInterval(s.MetricsInterval)
	if !session.MetricsMountOK(s.MetricsMount) {
		s.MetricsMount = ""
	}
	s.normalizeSchemes()
	s.Highlight.normalize()
	// A new map: the one held may be shared with the settings in force.
	var keys map[string]string
	for id, k := range s.Shortcuts {
		if _, ok := keymap.Parse(k); ok && id != "" {
			if keys == nil {
				keys = map[string]string{}
			}
			keys[id] = k
		}
	}
	s.Shortcuts = keys
	s.normalizeAI(d)
	s.normalizeBackups()
	if s.SessionLog.Dir = strings.TrimSpace(s.SessionLog.Dir); !filepath.IsAbs(s.SessionLog.Dir) {
		s.SessionLog.Dir = ""
	}
}

// MaxTerminalSchemes bounds the imported color schemes.
const MaxTerminalSchemes = 100

// normalizeSchemes keeps the valid imported schemes, one of each ID, and
// a chosen scheme that exists.
func (s *Settings) normalizeSchemes() {
	seen := map[string]bool{}
	var keep []theme.Scheme
	for _, sc := range s.TerminalSchemes {
		if sc.Custom() && sc.Valid() && !seen[sc.ID] && len(keep) < MaxTerminalSchemes {
			seen[sc.ID] = true
			keep = append(keep, sc)
		}
	}
	s.TerminalSchemes = keep
	if _, ok := theme.FindScheme(s.TerminalScheme, keep); !ok {
		s.TerminalScheme = ""
	}
}

// AddTerminalScheme keeps an imported scheme, in place of one of the same
// name (or of the oldest, when there are too many), and chooses it.
func (s *Settings) AddTerminalScheme(sc theme.Scheme) {
	s.TerminalSchemes = slices.DeleteFunc(slices.Clone(s.TerminalSchemes), func(o theme.Scheme) bool { return o.ID == sc.ID })
	if n := len(s.TerminalSchemes) - MaxTerminalSchemes + 1; n > 0 {
		s.TerminalSchemes = s.TerminalSchemes[n:]
	}
	s.TerminalSchemes = append(s.TerminalSchemes, sc)
	s.TerminalScheme = sc.ID
	s.normalizeSchemes()
}

// RemoveTerminalScheme forgets an imported scheme; the terminal goes back
// to the app's colors when it was chosen.
func (s *Settings) RemoveTerminalScheme(id string) {
	s.TerminalSchemes = slices.DeleteFunc(slices.Clone(s.TerminalSchemes), func(o theme.Scheme) bool { return o.ID == id })
	s.normalizeSchemes()
}

// aiModes are the permission modes a settings file may name.
var aiModes = map[string]bool{"ask": true, "review": true, "full": true}

func (s *Settings) normalizeAI(d Settings) {
	s.normalizeACP()
	s.AIDefaultAgent = strings.TrimSpace(s.AIDefaultAgent)
	if s.AIDefaultAgent == "" {
		s.AIDefaultAgent = d.AIDefaultAgent
	}
	s.AIDefaultMode = strings.TrimSpace(s.AIDefaultMode)
	if !aiModes[s.AIDefaultMode] {
		s.AIDefaultMode = d.AIDefaultMode
	}
	switch {
	case s.AIContextLines <= 0:
		s.AIContextLines = d.AIContextLines
	case s.AIContextLines < 50:
		s.AIContextLines = 50
	case s.AIContextLines > 2000:
		s.AIContextLines = 2000
	}
	switch {
	case s.AIAuditRetentionDays <= 0:
		s.AIAuditRetentionDays = d.AIAuditRetentionDays
	case s.AIAuditRetentionDays > 3650:
		s.AIAuditRetentionDays = 3650
	}
	switch {
	case s.AIRequestTimeout <= 0:
		s.AIRequestTimeout = d.AIRequestTimeout
	case s.AIRequestTimeout < AIRequestTimeoutMin:
		s.AIRequestTimeout = AIRequestTimeoutMin
	case s.AIRequestTimeout > AIRequestTimeoutMax:
		s.AIRequestTimeout = AIRequestTimeoutMax
	}
	// Unset keeps the built-in default; 0 is a choice (no limit) and stays.
	if s.AIMaxToolCalls != nil {
		switch n := *s.AIMaxToolCalls; {
		case n < 0:
			zero := 0
			s.AIMaxToolCalls = &zero
		case n > AIMaxToolCallsMax:
			v := AIMaxToolCallsMax
			s.AIMaxToolCalls = &v
		}
	}
	known := map[string]bool{}
	for i := range s.AIProviders {
		s.AIProviders[i].normalize()
		if id := s.AIProviders[i].ID; id != "" {
			known[id] = true
		}
	}
	s.normalizeReview()
	s.AIDefaultProvider = strings.TrimSpace(s.AIDefaultProvider)
	if p := s.provider(s.AIDefaultProvider); p == nil || !p.CanChat() {
		s.AIDefaultProvider = ""
	}
	s.AIConsentHosts = cleanList(s.AIConsentHosts)
	if s.MCPPort < 1024 || s.MCPPort > 65535 {
		s.MCPPort = d.MCPPort
	}
}

func (p *AIProvider) normalize() {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	p.Model = strings.TrimSpace(p.Model)
	p.Proxy = strings.TrimSpace(p.Proxy)
	switch p.Format {
	case AIFormatChat, AIFormatResponses, AIFormatAnthropic, AIFormatTypeSafe:
	case "":
		// A profile from before formats sent its limit as max_tokens.
		p.Format = AIFormatChat
		p.LegacyMaxTokens = p.LegacyMaxTokens || p.MaxTokens != nil
	default:
		p.Format = AIFormatChat
	}
	if p.Format != AIFormatChat {
		p.LegacyMaxTokens = false
	}
	if p.Format == AIFormatTypeSafe {
		// A decision takes no sampling parameters.
		p.Temperature, p.TopP, p.MaxTokens = nil, nil, nil
	}
	if p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) {
		p.Temperature = nil
	}
	if p.TopP != nil && (*p.TopP < 0 || *p.TopP > 1) {
		p.TopP = nil
	}
	if p.MaxTokens != nil {
		switch n := *p.MaxTokens; {
		case n <= 0:
			p.MaxTokens = nil
		case n > AIMaxTokensMax:
			v := AIMaxTokensMax
			p.MaxTokens = &v
		}
	}
	p.Models = cleanList(p.Models)
	if len(p.Models) > AIModelsMax {
		p.Models = p.Models[:AIModelsMax]
	}
	p.Headers = cleanHeaders(p.Headers)
}

func cleanList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ErrBaseURL reports a base URL that is not http(s) with a host, or that
// carries user info (a secret does not belong in the settings).
var ErrBaseURL = errors.New("settings: base URL must be http or https with a host")

// ErrPlainHTTP warns that a base URL is plain HTTP to a host that is not
// this computer. CheckBaseURL still returns a nil error for it.
var ErrPlainHTTP = errors.New("settings: plain http to a non-loopback host")

// ErrHeader reports a header name or value SSHYard will not send, including
// Authorization (the API key lives in the keychain).
var ErrHeader = errors.New("settings: invalid header")

// CheckBaseURL checks a provider base URL. warn is ErrPlainHTTP for plain
// http to a host outside this computer; err rejects anything that is not
// an http(s) URL with a host and no user info.
func CheckBaseURL(raw string) (warn, err error) {
	u, perr := url.Parse(strings.TrimSpace(raw))
	if perr != nil || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, ErrBaseURL
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return ErrPlainHTTP, nil
	}
	return nil, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CheckHeaders rejects a CR, LF or NUL in a name or value, an empty name,
// and Authorization (any case). The API key is not a header.
func CheckHeaders(h map[string]string) error {
	for k, v := range h {
		if headerBad(k) || headerBad(v) || credentialHeader(k) {
			return ErrHeader
		}
	}
	return nil
}

// credentialHeader reports a header that carries the API key, which only
// the key store may set.
func credentialHeader(k string) bool {
	k = strings.TrimSpace(k)
	return strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-Api-Key")
}

func headerBad(s string) bool {
	return strings.TrimSpace(s) == "" || strings.ContainsAny(s, "\r\n\x00")
}

// cleanHeaders drops headers CheckHeaders would reject. A provider loaded
// from disk must not be able to put the API key, or a split line, on the
// wire.
func cleanHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range h {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if headerBad(k) || strings.ContainsAny(v, "\r\n\x00") || credentialHeader(k) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fontName cleans a font family name: one family, as a comma would make
// a fallback list of it, of a sane length.
func fontName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 128 || strings.ContainsAny(s, ",;\n\r\t\x00") {
		return ""
	}
	return s
}

// Load reads the settings at path; a missing file gives the defaults.
func Load(path string) (Settings, error) {
	s := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Default(), err
	}
	s.Normalize()
	return s, nil
}

// Save writes the settings to path atomically.
func Save(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, b, 0o600)
}

// WriteFileAtomic writes data to a temporary file beside path and renames
// it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Settings) provider(id string) *AIProvider {
	if id == "" {
		return nil
	}
	for i := range s.AIProviders {
		if s.AIProviders[i].ID == id {
			return &s.AIProviders[i]
		}
	}
	return nil
}

// normalizeReview keeps a review its provider can give: a kind the
// provider's format allows, its first when none is, and a known strictness.
// A setting from before kinds asked the provider's chat model.
func (s *Settings) normalizeReview() {
	s.AIReviewProvider = strings.TrimSpace(s.AIReviewProvider)
	s.AIReviewModel = strings.TrimSpace(s.AIReviewModel)
	p := s.provider(s.AIReviewProvider)
	if p == nil {
		s.AIReviewProvider, s.AIReviewKind, s.AIReviewModel = "", "", ""
	} else if kinds := ReviewKinds(p.Format); !slices.Contains(kinds, s.AIReviewKind) {
		if s.AIReviewKind != "" {
			s.AIReviewModel = ""
		}
		s.AIReviewKind = kinds[0]
	}
	switch s.AIReviewStrictness {
	case AIReviewLoose, AIReviewStrict:
	default:
		s.AIReviewStrictness = AIReviewStandard
	}
}
