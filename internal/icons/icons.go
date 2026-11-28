// Package icons is SSHYard's icon set: SVGs from Remix Icon
// (https://remixicon.com, github.com/Remix-Design/RemixIcon, v4.8.0,
// Apache License 2.0; see LICENSE and NOTICE in this directory), embedded
// in the binary and drawn natively by MyGo's ui (ui.Icon), in the color of
// the text around them. No font, no webview.
//
// Interface/action icons come from here; AI brands use internal/aibrands.
// The "-line" style is the default;
// a "-fill" variant is used only for an active state (the settings button
// while the settings show, a favorite host, the active tab's kind). To add
// one, copy icons/<Category>/<name>.svg from the Remix Icon v4.8.0 tag into
// svg/ unchanged, and add a variable below with its file name.
package icons

import (
	"embed"
	"io/fs"
	"strings"

	"github.com/egoist/mygo/ui"
)

//go:embed svg/*.svg
var files embed.FS

// Icon is one parsed Remix Icon.
type Icon struct {
	Name string // the Remix Icon name, as "folder-3-line"
	SVG  *ui.SVG
}

var all = map[string]*Icon{}

func load(name string) *Icon {
	data, err := files.ReadFile("svg/" + name + ".svg")
	if err != nil {
		panic("icons: no Remix Icon " + name + " in svg/")
	}
	ic := &Icon{Name: name, SVG: ui.MustParseSVG(data)}
	all[name] = ic
	return ic
}

// The icons the app uses, by what they mean.
var (
	Host         = load("server-line")
	Add          = load("add-line")
	Settings     = load("settings-3-line")
	SettingsOn   = load("settings-3-fill")
	Star         = load("star-line")
	StarOn       = load("star-fill")
	Folder       = load("folder-3-line")
	FolderOn     = load("folder-3-fill")
	File         = load("file-3-line")
	SplitRight   = load("layout-column-line")
	SplitDown    = load("layout-row-line")
	Close        = load("close-line")
	Refresh      = load("refresh-line")
	Upload       = load("upload-2-line")
	UploadFolder = load("folder-upload-line")
	Download     = load("download-2-line")
	GoUp         = load("corner-left-up-line")
	Terminal     = load("terminal-box-line")
	TerminalOn   = load("terminal-box-fill")
	Edit         = load("edit-line")
	Delete       = load("delete-bin-line")
	Search       = load("search-line")
	TestLatency  = load("speed-up-line")
	ZoomIn       = load("zoom-in-line")
	ZoomOut      = load("zoom-out-line")
	Highlight    = load("mark-pen-line")
	Mouse        = load("mouse-line")
	NewFolder    = load("folder-add-line")
	Reconnect    = load("restart-line")
	Clear        = load("eraser-line")
	ClearText    = load("close-circle-line")
	LocalDisk    = load("computer-line")
	LocalDiskOn  = load("computer-fill")
	Folders      = load("layout-left-line")
	FoldersOn    = load("layout-left-fill")
	HideSidebar  = load("sidebar-fold-line")
	ShowSidebar  = load("sidebar-unfold-line")
	Rail         = load("layout-right-line")
	Warning      = load("error-warning-line")
	Appearance   = load("palette-line")
	Key          = load("key-2-line")
	Password     = load("lock-password-line")
	Generate     = load("magic-line")
	OpenFile     = load("folder-open-line")
	Completion   = load("magic-line")
	PathList     = load("folder-open-line")
	Paste        = load("clipboard-line")
	InstallKey   = load("shield-check-line")
	Proxy        = load("global-line")
	JumpHost     = load("route-line")
	Quick        = load("flashlight-line")
	Save         = load("save-line")
	Import       = load("import-line")
	Forward      = load("arrow-left-right-line")
	Run          = load("play-circle-line")
	RunOn        = load("play-circle-fill")
	Disconnect   = load("shut-down-line")
	Pause        = load("pause-line")
	Resume       = load("play-line")
	NewFile      = load("file-add-line")
	EditFile     = load("file-edit-line")
	EditFileOn   = load("file-edit-fill")
	OpenWith     = load("external-link-line")
	OpenWithApp  = load("apps-line")
	ExternalEdit = load("edit-box-line")
	MoveUp       = load("arrow-up-line")
	MoveDown     = load("arrow-down-line")
	ShowHidden   = load("eye-line")
	CopyPath     = load("file-copy-line")
	Chmod        = load("lock-line")
	Lock         = load("lock-line")
	Follow       = load("footprint-line")
	FollowOn     = load("footprint-fill")
	Collapse     = load("arrow-down-s-line")
	GroupOpen    = Collapse
	Expand       = load("arrow-up-s-line")
	Rename       = load("pencil-line")
	Robot        = load("robot-2-line")
	RobotOn      = load("robot-2-fill")
	NewChat      = load("chat-new-line")
	History      = load("history-line")
	Stop         = load("stop-circle-line")
	Back         = load("arrow-left-line")
	Info         = load("information-line")
	InfoOn       = load("information-fill")
	Monitor      = load("pulse-line")
	MonitorOn    = load("pulse-fill")
	Light        = load("sun-line")
	Dark         = load("moon-line")
	Shortcuts    = load("keyboard-line")
	CPU          = load("cpu-line")
	Memory       = load("ram-2-line")
	Disk         = load("hard-drive-3-line")
	Network      = load("arrow-up-down-line")
	GPU          = load("dashboard-3-line")
	Logins       = load("user-line")
	Sent         = load("arrow-up-line")
	Received     = load("arrow-down-line")
	Uptime       = load("time-line")
	Renewal      = load("calendar-schedule-line")
	Load         = load("speed-up-line")
	Privacy      = load("eye-off-line")
	PrivacyOn    = load("eye-off-fill")
	Transfers    = Network
	Relay        = Forward // a copy from one server straight to another
	LocalShell   = load("terminal-window-line")
	TelnetHost   = load("terminal-line")
	SerialHost   = load("usb-line")
	SendBreak    = load("pause-circle-line")
	// The window's own buttons, where the view draws them (Linux, Windows).
	WinMinimize = load("subtract-line")
	WinMaximize = load("checkbox-blank-line")
	WinRestore  = load("checkbox-multiple-blank-line")
	More        = load("more-line")
	Usage       = load("bar-chart-horizontal-line")
	UsageOn     = load("bar-chart-horizontal-fill")
	Docker      = load("ship-2-line")
	DockerOn    = load("ship-2-fill")
	Tmux        = load("stack-line")
	TmuxOn      = load("stack-fill")
	Detach      = load("logout-box-r-line")
	OpenInTab   = load("window-line")
	AppWindow   = OpenInTab
	NewWindow   = OpenInTab
	ToWindow    = load("share-box-line")
	Duplicate   = WinRestore
	Locate      = load("focus-3-line")
	Halt        = load("stop-line")
	Logs        = load("file-list-3-line")
	Backup      = load("safe-2-line")
	Update      = load("download-cloud-2-line")
	GitHub      = load("github-line")
	Feedback    = load("feedback-line")
	QQ          = load("qq-line")
	Check       = load("check-line")
	MoveTo      = load("folder-transfer-line")
	Submenu     = load("arrow-right-s-line")
	Next        = Submenu
	GroupClosed = Submenu
	Previous    = load("arrow-left-s-line")
	Copy        = load("file-copy-2-line")
	Compress    = load("file-zip-line")
	Extract     = load("inbox-unarchive-line")
	Image       = load("image-line")
	SelectAll   = load("checkbox-multiple-line")
	MultiSelect = SelectAll
	CollapseAll = load("collapse-diagonal-line")
	ExpandAll   = load("expand-diagonal-line")
	Send        = load("send-plane-2-line")
	Model       = load("brain-line")
	Thinking    = load("lightbulb-line")
	Attach      = load("attachment-line")
	FilePDF     = load("file-pdf-2-line")
	FileText    = load("file-text-line")
	FileTextOn  = load("file-text-fill")
	FileCode    = load("file-code-line")
	MCP         = load("plug-line")
	API         = load("code-s-slash-line")
	Skill       = load("tools-line")
	Certificate = load("shield-keyhole-line")
	HostKey     = load("shield-keyhole-line")
	Record      = load("record-circle-line")
	RecordOn    = load("record-circle-fill")
	Reset       = load("arrow-go-back-line")
	Command     = load("command-line")
	// Broadcast sends a command to several terminals at once.
	Broadcast   = load("broadcast-line")
	BroadcastOn = load("broadcast-fill")
	// Keyboard sends what is typed to several terminals at once.
	Keyboard   = load("keyboard-box-line")
	KeyboardOn = load("keyboard-box-fill")
	// The AI permission modes: asks first, reviewed, full access.
	ModeAsk    = InstallKey
	ModeReview = load("eye-line")
	ModeFull   = load("lock-unlock-line")
)

// Cloud backup: the section, backing up to a place, restoring from it,
// and the kinds of place.
var (
	Cloud         = load("cloud-line")
	CloudUpload   = load("upload-cloud-2-line")
	CloudDownload = Update
	BackupWebDAV  = Host
	BackupS3      = load("database-2-line")
	BackupGist    = GitHub
	BackupFolder  = MoveTo
)

// HostIcon is an icon a host can be given, by the key it is saved under.
type HostIcon struct {
	Key  string
	Icon *Icon
}

// HostIcons are the icons a host can be given. The first AutoHostIcons
// are the ones a host without its own is given, from its address.
var HostIcons = []HostIcon{
	{"server", Host},
	{"database", load("database-2-line")},
	{"cloud", load("cloud-line")},
	{"computer", LocalDisk},
	{"laptop", load("macbook-line")},
	{"router", load("router-line")},
	{"storage", load("hard-drive-2-line")},
	{"container", load("box-3-line")},
	{"code", load("code-s-slash-line")},
	{"rocket", load("rocket-line")},
	{"planet", load("planet-line")},
	{"test", load("flask-line")},
	{"git", load("git-branch-line")},
	{"home", load("home-4-line")},
	{"office", load("building-line")},
	{"secure", load("shield-line")},
	{"game", load("gamepad-line")},
	{"ubuntu", load("ubuntu-line")},
	{"centos", load("centos-line")},
	{"apple", load("apple-line")},
	{"windows", load("windows-line")},
	{"android", load("android-line")},
}

// AutoHostIcons is how many of HostIcons, from the first, a host may be
// given without choosing: those that say nothing of what it runs.
const AutoHostIcons = 11

// HostIconOf is the host icon of key.
func HostIconOf(key string) (*Icon, bool) {
	for _, h := range HostIcons {
		if h.Key == key {
			return h.Icon, true
		}
	}
	return nil, false
}

// Get returns an icon by its Remix Icon name.
func Get(name string) (*Icon, bool) {
	ic, ok := all[name]
	return ic, ok
}

// Names lists the embedded icon files' names.
func Names() []string {
	var out []string
	es, _ := fs.ReadDir(files, "svg")
	for _, e := range es {
		out = append(out, strings.TrimSuffix(e.Name(), ".svg"))
	}
	return out
}
