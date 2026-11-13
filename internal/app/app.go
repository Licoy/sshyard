// Package app is SSHYard's interface: the host sidebar, the tabs and split
// panes (native terminals and files browsers alike) and the settings, built with
// MyGo's immediate-mode ui package. Everything platform-specific (the
// window's theme and language, the keychain, the updater, file dialogs)
// comes in through Platform, so the whole interface runs in ui.Tester.
package app

import (
	"cmp"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appicon "github.com/Licoy/sshyard/icon"
	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/audit"
	"github.com/Licoy/sshyard/internal/ai/history"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/forward"
	"github.com/Licoy/sshyard/internal/highlight"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/localkeys"
	"github.com/Licoy/sshyard/internal/netproxy"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/termlog"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/updater"
	"github.com/Licoy/sshyard/internal/vault"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// Platform is what the app needs from the system, including an explicit
// system keychain and a persistent data directory for encrypted secrets.
type Platform struct {
	// Post asynchronously schedules a callback on the application UI thread,
	// even with every window hidden; it must never run the callback inline.
	// When nil, queued callbacks run before the next View frame (ui.Tester).
	Post func(func())
	// SetTrayIcon changes the system tray icon immediately; an error leaves it unchanged.
	SetTrayIcon func(style string) error
	// SetRememberLayout changes native geometry persistence; errors leave it unchanged.
	// A successful change returns an undo that restores its exact previous state.
	SetRememberLayout func(bool) (undo func() error, err error)
	// OpenPath opens a file in its default app; nil can't.
	OpenPath func(path string) error
	// Notify shows a system notification, replacing the one of the same
	// id, and calls clicked as it is clicked; it may block, and is never
	// called on a frame. nil can't.
	Notify func(id, title, body string, clicked func()) error
	// CacheDir is where files opened in another app are copied, the
	// user's cache folder when "".
	CacheDir string
	// DataDir holds application metadata and encrypted credentials; it is required
	// unless ResolveStorage supplies the persistent storage paths.
	DataDir string
	Version string
	// Secrets stores only the random master key; failures keep the app locked.
	Secrets        keychain.Backend
	SecretLockPath string
	SecretScope    string
	StartupError   error
	ResolveStorage func() (StoragePaths, error)
	Updates        updater.Backend
	// SetTheme and SetLocale apply the user's choice to the window.
	SetTheme  func(theme.Mode)
	SetLocale func(tag string)
	// PickFile shows the system's open dialog; it may block.
	PickFile func(title string) (string, error)
	// PickFiles is PickFile choosing several files.
	PickFiles func(title string) ([]string, error)
	// SaveFile shows the system's save dialog, at path to begin with; it
	// may block. "" with no error is a cancel.
	SaveFile func(title, path string) (string, error)
	// PickFolder shows the system's open dialog for a folder, in start to
	// begin with; it may block. "" with no error is a cancel.
	PickFolder func(title, start string) (string, error)
	// OpenWith lets the user choose the app that opens path, then opens
	// it there; it may block. A cancel is no error. nil can't.
	OpenWith func(path, title string) error
	// OpenWindow opens a window of its own showing spec's view, for the
	// file editor; it may block, and is never called on a frame. nil
	// can't.
	OpenWindow func(spec WindowSpec) Window
	// Dial replaces net.Dialer, for tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// HomeDir is where the local side of a files pane starts, and whose
	// .ssh/config the import reads; "" is the user's home.
	HomeDir string
	// LocalUser is the login name a host without one defaults to, as ssh
	// does; "" is the user running the app.
	LocalUser string
}

// WindowSpec is a window the app opens beside the main one.
type WindowSpec struct {
	Title         string
	Width, Height int
	// StateKey remembers the window's size and place between launches.
	StateKey string
	View     func(*ui.Context)
	// Closing is asked when the user closes the window; false keeps it
	// open. It runs on no frame.
	Closing func() bool
	// Closed is told once the window is gone.
	Closed func()
}

// Window is a window OpenWindow opened. Its methods may be called on no
// frame only.
type Window interface {
	Focus()
	Close()
	SetTitle(string)
	// SetEdited marks the window as holding unsaved changes.
	SetEdited(bool)
}

type page int

const (
	// pageWorkspace is the tabs and their panes.
	pageWorkspace page = iota
	pageSettings
)

// conn is what one pane shows, a terminal or a files browser, with its
// session: a shell, or for files no shell, on the host's connection that
// the panes on the host share (see session.Pool). Each pane still lives,
// reconnects and closes on its own.
type conn struct {
	waitingSecrets bool
	restorePending bool
	restoredQuiet  bool
	unattended     bool // no one watches it start: a login asking questions fails
	id             int
	kind           layout.Kind
	host           assets.Host
	s              *session.Session
	tv             *termView   // terminal panes
	fs             *filesState // files and editor panes (its SFTP client)
	ed             *edWin      // editor windows
	// handled is the failure a prompt was already shown for
	handled error
	// tried is, by a.typed key, the generation of the secret the last
	// attempt used (one per hop of a jump chain), so that a failure with
	// a secret since replaced does not ask again. Under App.mu.
	tried map[string]int
	// fw is the port forward the connection serves, shown in no pane
	fw *forward.Forward
	// cwd is the directory a terminal last reported, cwdSeq when (from
	// App.cwdSeq), and up since when it is connected
	cwd    string
	cwdSeq int
	up     time.Time
	// name is what the server says its hostname is, asked once per
	// connect (nameConn) when a report names a host; see cwdOnHost
	name     string
	nameAsk  nameState
	nameConn int
	// probe is how far reading the shell's directory from the server
	// went, for a terminal that does not report it, per connect
	// (probeConn), probeAt when it was last read and probeMiss how many
	// reads in a row found nothing; see probeCwd
	probe     probeState
	probeConn int
	probeAt   time.Time
	probeMiss int
	// start is what an SSH terminal's shell is given each time it starts
	// (see shellStart); tmuxConn is the connect it was last seen at,
	// tmuxUp when, and tmuxAt when the tmux session the terminal is in
	// was last read (tmuxReading while it is); see watchTmux
	start       *shellStart
	tmuxConn    int
	tmuxUp      time.Time
	tmuxAt      time.Time
	tmuxReading bool
	// temp is the ID of the temporary host the pane was opened on, kept
	// after it is saved as a host, so that what was typed for it goes
	// when its last pane closes
	temp string
	// termNo is this pane's number among the host's terminal panes, in
	// the order they were opened, from 1. It stays until the pane
	// closes. A freed number is not reused while any other terminal pane
	// of that host is open; the count starts over when the last one
	// closes. Files panes are not numbered. 0 is not a terminal pane.
	termNo int
	// metCPU, metDisk, metNet, metGPU and metLoad are this pane's metrics
	// popovers; metMore is the popover of the items the bar has no room
	// for, metMoreItem the key of the item whose details it shows.
	metCPU, metDisk, metNet, metGPU, metLoad, metMore bool
	metMoreItem                                       string
	// metCompact drops the words of the metrics bar, for a pane too
	// narrow for them (metricsCompact)
	metCompact bool
	// metW is the width each item of the metrics bar took when last
	// shown, by key, for laying out the bar before it is shown;
	// metShown is metCompact as the bar was last shown, the mode of the
	// widths the last frame measured
	metW     map[string]float32
	metShown bool
	// ai is set for an AI pane. It has no session of its own.
	ai *aiState
	// log is a terminal's session log while it is kept (startLog);
	// logConns is how many times the session had connected as watchLogs
	// last looked, and autoLog that its host's log was started once.
	log      *termlog.Recorder
	logConns int
	autoLog  bool
}

func (c *conn) item() layout.Item { return layout.Item{Kind: c.kind, ID: c.id} }

// close ends the connection and what runs on it.
func (c *conn) close() {
	if r := c.log; r != nil {
		c.log = nil
		c.s.Term.SetLineHook(nil)
		c.s.SetOutputTap(nil)
		r.Close(c.s.Term.CursorLine(), time.Now())
	}
	if c.fs != nil {
		c.fs.close()
	}
	if c.fw != nil {
		c.fw.Stop()
		return
	}
	if c.s != nil {
		c.s.Close()
	}
}

type sessTarget struct{ s *session.Session }

func (t sessTarget) Terminal() *vt.Terminal { return t.s.Term }
func (t sessTarget) Resize(cols, rows int)  { t.s.Resize(cols, rows) }

// Write sends typed input without waiting for the connection.
func (t sessTarget) Write(p []byte) (int, error) {
	if err := t.s.Send(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// App is the interface's state. Its View runs on the UI thread; work in
// the background posts its results with post.
type App struct {
	p          Platform
	logo       *ui.Bitmap
	settings   settings.Settings
	background backgroundState
	store      *assets.Store
	kc         *keychain.Keychain
	keychainOK bool
	unlock     unlockState
	known      *session.KnownHosts
	ws         *layout.Workspace
	conns      map[int]*conn
	// cwdSeq counts the directory reports of terminals
	cwdSeq   int
	nextConn int
	// pool shares one authenticated connection per host among its panes
	pool *session.Pool
	// typed are the secrets typed this run and never saved, by
	// promptKey: an "ask" host's password, a passphrase asked each run, a
	// password typed after the saved one was refused. Under mu:
	// credentials read them off the UI thread.
	typed map[string]string
	// typedGen counts the answers to the prompt for each key. Under mu.
	typedGen map[string]int
	// skipKeys are the KeyIDs of the keys of this computer the user
	// passed over this run. Under mu.
	skipKeys map[string]bool
	// rememberKeys are the keys whose typed passphrase is remembered once
	// it logged in. Under mu.
	rememberKeys map[string]localkeys.Remembered
	// lk is what is kept about the keys of this computer; lkOn and lkMCP
	// are settings.LocalKeys and LocalKeysMCP, read off the UI thread.
	lk          *localkeys.State
	lkOn, lkMCP atomic.Bool
	upd         updater.Backend

	mu     sync.Mutex
	queue  []func()
	posted bool
	closed bool
	svc    ui.Services
	live   bool

	attachmentPreview *aiAttachmentPreview

	// interface state
	page page
	// pref is the settings page showing, prefSearch the settings' search
	pref       prefPage
	prefSearch prefSearchState
	snippets   snippetState
	// bc is the broadcast panel's state.
	bc broadcastState
	// tabStart is the first tab the title bar shows when not all fit, the
	// others in its menu of more tabs; tabRoom the width they had
	tabStart int
	tabRoom  float32
	// tabWheeled is set once the wheel moves the tabs shown, which then
	// stay where it left them until another tab than tabKept is active;
	// tabWheel is what of a touchpad's scroll has yet to move them by one
	tabWheeled bool
	tabKept    int
	tabWheel   float32
	// tabsOpen shows the popover of the tabs that do not fit
	tabsOpen bool
	// menu is the drop-down or context menu open (menuButton,
	// contextMenu), ctx the context menu's place and entries
	menu dropMenu
	ctx  ctxMenu
	// card is the sidebar host whose card shows, or soon will
	card cardHover
	// clearContent is set while the content shows the window's material,
	// as the appearance settings ask (see contentFill)
	clearContent bool
	// tmuxWarned are the hosts whose terminals were told that their
	// server has no tmux, by ID
	tmuxWarned map[string]bool
	// usage, docker, tmux and hist are what the usage, Docker, tmux and
	// command history panels read
	usage       *usageState
	docker      *dockerState
	tmux        *tmuxState
	hist        *historyState
	selected    string // host chosen in the sidebar
	search      string
	searchFocus bool // the search, its field or button, takes the focus next frame
	searchOpen  bool // the search shows its field across the toolbar
	// ping is the hosts' latency the sidebar shows
	ping pingState
	// multi is the sidebar choosing several hosts and groups
	multi       multiSelect
	sections    map[string]*bool
	form        *hostForm
	editing     bool
	deleting    bool
	deleteID    string
	prompt      *credPrompt
	promptHold  focusHold
	editorHold  focusHold
	credHold    focusHold
	proxyHold   focusHold
	installHold focusHold
	// challenges are the rounds of login questions (one-time passwords)
	// waiting for the user, the first in the sheet; see challenge.go
	challenges    []*challengeAsk
	challenging   bool
	challengeHold focusHold
	challengeSeq  int
	// credForm is the credential sheet's fields; credDelete the
	// credential the delete dialog is for, and rebindChoice where its
	// hosts go
	credForm    *credForm
	credEditing bool
	// proxyForm is the proxy sheet open, from settings or the host editor
	proxyForm    *proxyForm
	proxyEditing bool
	// proxyClients are the HTTP clients of proxyClient by proxy choice,
	// under mu
	proxyClients map[string]*http.Client
	credDelete   string
	credDeleting bool
	rebindChoice string
	install      *installForm
	installing   bool
	mkdirHold    focusHold
	compressHold focusHold
	prompting    bool
	sudo         *sudoAsk // the sheet asking a files pane's sudo password
	sudoAsking   bool
	sudoHold     focusHold
	hostKey      *hostKeyAsk
	keyChange    *hostKeyChange
	changingKey  bool
	khQuery      string
	askingKey    bool
	field        welcomeField // the welcome page's dots
	finding      bool
	query        string
	match        int
	focusReq     int // session whose terminal takes the focus next frame
	pct          map[int]*float32
	langChoice   string
	fontChoice   float64
	keepChoice   float64
	// the terminal's row height and cell width, in percent, as edited
	lineChoice, widthChoice float64
	// toasts are shown at the next frame, said from outside one
	toasts []func(m locales.Messages) ui.Toast
	// chatList are the saved AI conversations of each host, as last read.
	chatList map[string][]history.Summary
	// the transfers at once and the size to confirm opening, as edited
	concChoice, openMBChoice float64
	osc52Choice              int
	filesEdgeChoice          int // where a files pane opens, by index
	toolPanesChoice          int // how the rail opens its tools, by index
	rightClickChoice         int // what a terminal's right click does, by index
	chromeMat, contentMat    int // the materials chosen, by materials' index
	osc52KiB                 float64
	clip                     clipGate
	clipAsk                  *oscAsk
	// sbWidth is the sidebar's width; sbSeen and sbAt are its value last
	// seen and since when, to save it once it rests.
	sbWidth, sbSeen float32
	sbAt            time.Time
	// filesSplit is the first side's share, in percent, of the files
	// panes showing both sides; splitSeen and splitAt are its value last
	// seen and since when, to save it once it rests.
	filesSplit, splitSeen float32
	splitAt               time.Time
	// winSvc are the services of the editor windows, by their conn,
	// which invalidate redraws too. Under mu.
	winSvc map[int]ui.Services
	// win is the window's buttons where the app draws them (see
	// WindowControls), maximized whether the window is
	win         *WindowControls
	maximized   bool
	updateCheck bool
	// updEvery is the index of the update interval in its control;
	// updProxy the updates' proxy choice in its select.
	updEvery  int
	updProxy  string
	themeIdx  int
	autoRecon bool
	// lkChoice and lkMCPChoice are the switches of settings.LocalKeys and
	// LocalKeysMCP.
	lkChoice, lkMCPChoice bool
	fonts                 fontState
	zoom                  zoomState
	// hl colors the keywords of the terminals' output as settings.Highlight
	// says; nil until built, again after it changes.
	hl *highlight.Highlighter
	// hlForm is the keyword highlighting rule sheet's draft while
	// hlEditing; hlHold keeps its pattern focused as it opens.
	hlForm    *hlRuleForm
	hlEditing bool
	hlHold    focusHold
	// paneBounds are where the panes were last frame, by session
	paneBounds map[int]ui.Rect
	// barWidths are how wide the actions an actionBar keeps drew last
	// frame, by bar and action
	barWidths map[string]float32
	// dropPreview is where a drag in progress would land, nil for none
	dropPreview *dropTarget
	// temps are the temporary hosts of quick connect, by ID, kept while a
	// pane uses them and never saved; nextTemp numbers them
	temps         map[string]assets.Host
	nextTemp      int
	quick         *quickForm
	quicking      bool
	quickHold     focusHold
	migration     *migrationForm
	migrationOpen bool
	migrationHold focusHold
	// palette is the command palette, open while palette.open
	palette paletteState
	// recording is the action whose shortcut is being recorded, and
	// recordErr why the last key pressed for it could not be its shortcut
	recording, recordErr string
	imp                  *importForm
	importing            bool
	impHold              focusHold
	// fwds are the port forwards running, by forward ID
	fwds       map[string]*fwdRun
	fwdForm    *fwdForm
	fwdEditing bool
	fwdHold    focusHold
	// fwdDeleting asks before deleting the forward fwdDeleteID.
	fwdDeleting bool
	fwdDeleteID string
	// fwdOpen are the forwards whose connections the sidebar lists
	fwdOpen map[string]bool
	// termSeq is the next terminal-pane number per host. See conn.termNo.
	termSeq map[string]int
	// metrics are the host collectors, by the host's pool key. One per
	// host, shared by its terminal panes. The channel is not a pool
	// reference. metMu guards the map and minimized; sessions stay on
	// the UI thread.
	metrics   map[string]*session.Collector
	metMu     sync.Mutex
	minimized bool
	// metOn, metEvery and metMount are the settings controls.
	// metMountFocus is set while the mount field has the keyboard, so
	// leaving it or the settings page can save what was typed.
	metOn         bool
	metEvery      int
	metMount      string
	metMountFocus bool
	// termCustom shows the terminal type setting's custom field, termText
	// its text; termFocus is set while it has the keyboard (termTypeRow).
	termCustom bool
	termText   string
	termFocus  bool
	// newGate, newTools and newModel are test seams. Nil uses
	// policy.NewGate, the host's remote tools, and the OpenAI client.
	newGate  func(mode func() policy.Mode, ask policy.Prompter) policy.Gate
	newTools func(*conn) agent.Tools
	newModel func(*conn) agent.Streamer
	// audit is one log for every pane, so concurrent writes share its lock.
	audit *audit.Log
	// aiRecent is the AI pane last used on a host, by host ID.
	aiRecent map[string]int
	aiLines  float64
	aiDays   float64
	// aiWait is the AI request timeout field, in seconds.
	aiWait float64
	// aiCalls is the tool-call limit field, empty for the default.
	aiCalls   string
	aiModeIdx int
	// review is the review model's settings controls.
	review   reviewControls
	aiHold   focusHold
	fullHold focusHold
	full     *fullAsk
	consent  *consentAsk
	prov     *providerForm
	provOpen bool
	acpForm  *acpForm
	acpOpen  bool
	acpWG    sync.WaitGroup
	// acpProbes are the configured ACP agents' selectors, by agent id.
	acpProbes map[string]*acpProbe
	// provDelete asks before removing a provider and its keychain key.
	provDeleting bool
	provDeleteID string
	// askToasts are the toasts telling of approvals waiting in panes out
	// of sight, by pane, with the approval each tells of.
	askToasts map[int]*aiPend
	// mcp is the agents' server on this computer (open API, MCP and
	// skill); mcpAsk the
	// risk notice shown before it is turned on, mcpPort the port as edited
	mcp     mcpState
	mcpAsk  *mcpAsk
	mcpHold focusHold
	mcpPort float64
	// closing is set while Close saves the layout, so each pane does not.
	closing bool
	// restoring is set while a saved layout is opened, so it is not saved
	// again over itself mid-way.
	restoring        bool
	layoutBlocked    bool
	layoutSaveFailed bool
	layoutObserved   []byte
	layoutDue        time.Time
	// localName is what local terminals are called, in the language of
	// the last frame.
	localName string
	// logs is the session logs pane's.
	logs logsState
	// toolNames are what the tool panes are called, in the language of
	// the last frame; toolFocus is the pane each tab focused last, by tab
	// ID, which a tool pane gives the focus back to.
	toolNames map[layout.Kind]string
	toolFocus map[int]int
	// railWidths are how wide, in DIPs, the panes the rail opens were
	// last left beside another pane, by kind; see fitRailWidth.
	railWidths map[layout.Kind]float32
	// shells are this computer's shells, found as the app starts; see
	// localID.
	shells []session.Shell
	// group is the group sheet open (new or rename), groupDeleting asks
	// before deleting groupDelete
	group         *groupForm
	grouping      bool
	groupHold     focusHold
	groupDeleting bool
	groupDelete   string
	// renaming is the tab rename sheet open, tabRename its tab and name
	tabRename     *tabRenameForm
	renaming      bool
	tabRenameHold focusHold
	// backup is the backup sheet open, to export or to restore; kdf is
	// what its password is derived with (cheaper in tests)
	backup     *backupForm
	backingUp  bool
	backupHold focusHold
	kdf        vault.Params
	// cost is the sheet asking whether to derive a backup's or a PuTTY
	// key's password whose derivation costs more than usual; costKeys
	// the keys derived once agreed, by costlyID under costSalt
	cost     *costAsk
	costOpen bool
	costHold focusHold
	costKeys map[[32]byte]costlyKey
	costSalt []byte
	// defaultPw is the sheet setting the default backup password
	defaultPw        *passwordForm
	settingDefaultPw bool
	defaultPwHold    focusHold
	// masterPw is the local master password sheet: to unlock, set it up
	// in place of an unusable system keychain, change it or leave it
	masterPw     *masterPwForm
	masterPwOpen bool
	masterPwHold focusHold
	// cloud is what this device knows of the places it backs up to, read
	// once; cloudCtx ends their requests when the app closes. target is
	// the backup location sheet open, remotes the sheet of a place's
	// backups. gistAPI replaces GitHub's API, for tests.
	cloud         *cloudState
	cloudCtx      context.Context
	cloudStop     context.CancelFunc
	target        *targetForm
	targetEditing bool
	targetHold    focusHold
	remotes       *remoteList
	listingRemote bool
	gistAPI       string
}

// New opens the app's data.
func New(p Platform) (*App, error) {
	a := &App{p: p, ws: layout.New(), conns: map[int]*conn{}, pool: session.NewPool(), typed: map[string]string{}, typedGen: map[string]int{}, skipKeys: map[string]bool{}, rememberKeys: map[string]localkeys.Remembered{}, lk: localkeys.Open(""), sections: map[string]*bool{}, temps: map[string]assets.Host{}, pct: map[int]*float32{}, fwds: map[string]*fwdRun{}, fwdOpen: map[string]bool{}, paneBounds: map[int]ui.Rect{}, metrics: map[string]*session.Collector{}, aiRecent: map[string]int{}}
	a.logo = appicon.Bitmap()
	a.settings = settings.Default()
	a.shells = detectShells()
	a.kdf = vault.DefaultParams
	a.cloudCtx, a.cloudStop = context.WithCancel(context.Background())
	a.kc = keychain.New(lockedSecrets{})
	a.upd = p.Updates
	if a.upd == nil {
		a.upd = disabledUpdates{}
	}
	a.upd.OnChange(a.invalidate)
	err := a.prepareStorage()
	a.unlock.err = err
	a.syncChoices()
	a.resolveFonts(false, false, false)
	a.updateCheck = a.settings.CheckUpdates
	return a, err
}

func (a *App) loadData() error {
	p := a.p
	var errs []error
	path := func(name string) string {
		if p.DataDir == "" {
			return ""
		}
		return filepath.Join(p.DataDir, name)
	}
	if p.DataDir != "" {
		if err := os.MkdirAll(p.DataDir, 0o700); err != nil {
			errs = append(errs, err)
		}
		if s, err := settings.Load(path("settings.json")); err == nil {
			a.settings = s
		} else {
			errs = append(errs, err)
		}
	}
	a.lk = localkeys.Open(path("localkeys.json"))
	st, err := assets.Open(path("hosts.json"))
	if err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}
	a.store = st
	if kh := path("known_hosts"); kh != "" {
		a.known, err = session.OpenKnownHosts(kh)
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

type disabledUpdates struct{}

func (disabledUpdates) Enabled() bool                { return false }
func (disabledUpdates) Check()                       {}
func (disabledUpdates) SetAutomatic(bool)            {}
func (disabledUpdates) SetRoute(updater.Route)       {}
func (disabledUpdates) Available() string            { return "" }
func (disabledUpdates) OnChange(func()) (off func()) { return func() {} }

func (a *App) syncChoices() {
	a.lkOn.Store(a.settings.LocalKeys)
	a.lkMCP.Store(a.settings.LocalKeys && a.settings.LocalKeysMCP)
	a.langChoice = a.settings.Language
	a.fontChoice = float64(a.settings.TerminalFontSize)
	a.lineChoice = float64(a.settings.TerminalLineHeight)
	a.widthChoice = float64(a.settings.TerminalCharWidth)
	a.keepChoice = float64(a.settings.KeepAliveSeconds)
	a.concChoice = float64(a.settings.TransferConcurrency)
	a.openMBChoice = float64(a.settings.OpenConfirmMB)
	a.osc52KiB = float64(a.settings.OSC52MaxBytes >> 10)
	a.sbWidth = float32(a.settings.SidebarWidth)
	a.filesSplit = a.settings.FilesSplit
	a.metOn = a.settings.MetricsEnabled
	a.metEvery = intervalIndex(a.settings.MetricsInterval)
	a.updEvery = max(slices.Index(settings.UpdateIntervals, a.settings.UpdateIntervalHours), 0)
	a.metMount = a.settings.MetricsMount
	a.aiLines = float64(a.settings.AIContextLines)
	a.aiDays = float64(a.settings.AIAuditRetentionDays)
	a.aiWait = float64(a.settings.AIRequestTimeout)
	a.aiCalls = maxToolCallsText(a.settings.AIMaxToolCalls)
	a.mcpPort = float64(a.settings.MCPPort)
}

// Settings returns the settings in effect.
func (a *App) Settings() settings.Settings { return a.settings }

// Store returns the host store.
func (a *App) Store() *assets.Store { return a.store }

// Keychain returns the keychain in use, and whether it is the system's.
func (a *App) Keychain() (*keychain.Keychain, bool) { return a.kc, a.keychainOK }

// ApplySettings applies and saves settings.
func (a *App) ApplySettings(s settings.Settings) error {
	if !a.ready() {
		return keychain.ErrLocked
	}
	s.Normalize()
	old := a.settings
	iconChanged := s.TrayIcon != old.TrayIcon && a.p.SetTrayIcon != nil
	if iconChanged {
		if err := a.p.SetTrayIcon(s.TrayIcon); err != nil {
			return err
		}
	}
	if a.p.DataDir != "" {
		if err := a.saveSettingsLayout(s, old); err != nil {
			if iconChanged {
				err = errors.Join(err, a.p.SetTrayIcon(old.TrayIcon))
			}
			return err
		}
	}
	a.settings = s
	if old.Background != s.Background {
		a.background.preview = nil
		a.invalidate()
	}
	for _, c := range a.conns {
		if c.ai == nil {
			continue
		}
		oldACP, _ := old.ACP(c.ai.agent)
		newACP, _ := s.ACP(c.ai.agent)
		all := settings.IsACP(c.ai.agent) && !oldACP.SameLaunch(newACP) ||
			old.AIReviewProvider != s.AIReviewProvider || old.AIReviewKind != s.AIReviewKind ||
			old.AIReviewModel != s.AIReviewModel || old.AIReviewStrictness != s.AIReviewStrictness ||
			old.AIRedact != s.AIRedact || old.AIDefaultMode != s.AIDefaultMode
		if all || c.ai.agent == "builtin" && !reflect.DeepEqual(providerIn(old, c.ai.provider), providerIn(s, c.ai.provider)) || !reflect.DeepEqual(providerIn(old, old.AIReviewProvider), providerIn(s, s.AIReviewProvider)) {
			a.invalidateAI(c)
		} else if c.ai.agent == "builtin" && old.MaxToolCalls() != s.MaxToolCalls() {
			// The built-in agent reads the limit as a turn starts or
			// continues, so the turn running is left to finish; an
			// external agent counts its own calls.
			c.ai.dirty = true
		}
	}
	if s.Theme != old.Theme && a.p.SetTheme != nil {
		a.p.SetTheme(s.Theme)
	}
	if s.Language != old.Language && a.p.SetLocale != nil {
		a.p.SetLocale(s.Language)
	}
	if !reflect.DeepEqual(s.Highlight, old.Highlight) {
		a.hl = nil
	}
	if s.TerminalFontSize != old.TerminalFontSize || s.TerminalLineHeight != old.TerminalLineHeight || s.TerminalCharWidth != old.TerminalCharWidth {
		for _, c := range a.conns {
			if c.tv != nil {
				c.tv.fontSize = float32(s.TerminalFontSize)
				a.spaceTerm(c.tv)
			}
		}
	}
	if s.TransferConcurrency != old.TransferConcurrency {
		for _, c := range a.conns {
			if c.fs != nil {
				c.fs.transfers.SetConcurrency(s.TransferConcurrency)
			}
		}
	}
	if s.OSC52MaxBytes != old.OSC52MaxBytes {
		for _, c := range a.conns {
			if c.tv != nil && c.s != nil && c.s.Term != nil {
				c.s.Term.SetOSC52Max(s.OSC52MaxBytes)
			}
		}
	}
	if uiF, termF, fbF := s.UIFont != old.UIFont, s.TerminalFont != old.TerminalFont, s.TerminalFallbackFont != old.TerminalFallbackFont; uiF || termF || fbF {
		a.resolveFonts(uiF, termF, fbF)
	}
	if s.MCPEnabled != old.MCPEnabled || s.MCPPort != old.MCPPort {
		a.syncMCP()
	}
	if s.CheckUpdates != old.CheckUpdates {
		a.upd.SetAutomatic(s.CheckUpdates)
	}
	if s.UpdateProxy != old.UpdateProxy {
		a.routeUpdates()
	}
	a.syncChoices()
	return nil
}

// Start applies the saved theme and language to the window.
func (a *App) Start() {
	a.loadFonts()
	a.probeDesktop()
	if a.p.SetTheme != nil {
		a.p.SetTheme(a.settings.Theme)
	}
	if a.p.SetLocale != nil && a.settings.Language != "" {
		a.p.SetLocale(a.settings.Language)
	}
	if !a.ready() {
		a.unlock.modal = true
		return
	}
	a.startIndependent()
	a.probeUnlock()
}

func (a *App) applyWindowSettings() {
	if a.p.SetTheme != nil {
		a.p.SetTheme(a.settings.Theme)
	}
	if a.p.SetLocale != nil && a.settings.Language != "" {
		a.p.SetLocale(a.settings.Language)
	}
}

// startIndependent starts work that does not access saved secrets.
func (a *App) startIndependent() {
	if a.unlock.started {
		return
	}
	a.unlock.started = true
	a.loadLayout()
	if a.settings.OpenLocalOnStart {
		a.post(func() {
			if len(a.ws.Tabs) == 0 {
				a.OpenLocalTerminal()
			}
		})
	}
	if root, err := a.cacheDir(); err == nil {
		go cleanCache(root)
	}
	a.startRenewalReminders()
}

// post runs fn on the UI thread, without requiring a visible window.
func (a *App) post(fn func()) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.queue = append(a.queue, fn)
	schedule := a.p.Post != nil && !a.posted
	if schedule {
		a.posted = true
	}
	a.mu.Unlock()
	if schedule {
		a.p.Post(func() {
			a.mu.Lock()
			a.posted = false
			a.mu.Unlock()
			a.drain()
			a.invalidate()
		})
	}
	a.invalidate()
}

func (a *App) invalidate() {
	a.mu.Lock()
	svc, live := a.svc, a.live
	wins := make([]ui.Services, 0, len(a.winSvc))
	for _, w := range a.winSvc {
		wins = append(wins, w)
	}
	a.mu.Unlock()
	if live {
		svc.Invalidate()
	}
	for _, w := range wins {
		w.Invalidate()
	}
}

func (a *App) drain() {
	a.mu.Lock()
	q := a.queue
	a.queue = nil
	a.mu.Unlock()
	for _, fn := range q {
		a.mu.Lock()
		closed := a.closed
		a.mu.Unlock()
		if closed {
			return
		}
		fn()
	}
}

// Close ends every session and port forward.
func (a *App) Close() {
	a.closeBackground()
	a.mu.Lock()
	a.closed = true
	a.queue = nil
	a.mu.Unlock()
	a.unlock.mu.Lock()
	defer a.unlock.mu.Unlock()
	a.unlock.closed = true
	a.unlock.after = nil
	a.cloudStop()
	a.stopCollectors()
	a.stopMCP()
	a.waitMCP()
	if !a.closing {
		a.closing = true
		a.saveLayout()
	}
	for _, c := range a.conns {
		if c.kind == layout.AI {
			a.saveChat(c)
		}
	}
	for id, c := range a.conns {
		a.clip.forget(id)
		if c.kind == layout.AI {
			a.StopAI(c.id)
		}
		c.close()
	}
	a.stopACPProbes()
	a.waitACP()
	a.clipAsk = nil
	a.fwds = map[string]*fwdRun{}
	if a.kc != nil && (!a.unlock.busy || a.unlock.completed) {
		a.closeSecrets(a.kc)
	}
	a.unlock.ready.Store(false)
}

// Open where a new session goes.
type openAt struct {
	pane int // 0 for a new tab
	edge layout.Edge
}

// Connect opens a terminal to a saved host, in a new tab or on a pane.
func (a *App) Connect(hostID string, at openAt) (int, error) {
	return a.open(hostID, layout.Terminal, at, nil)
}

// OpenFiles opens a files pane on a saved host, in a new tab or on a
// pane. from, when set, is the pane it was opened from (its host's
// connection, and secrets typed for it, serve every pane on the host).
func (a *App) OpenFiles(hostID string, at openAt, from *conn) (int, error) {
	return a.open(hostID, layout.Files, at, from)
}

func (a *App) open(hostID string, kind layout.Kind, at openAt, from *conn) (int, error) {
	if ex := a.singleAt(kind, at); ex != nil {
		return a.reuse(ex, hostID, 0)
	}
	c, err := a.makeConn(hostID, kind, 0)
	var inUse *serialInUseError
	if errors.As(err, &inUse) {
		// A serial port opens once: its pane is the host's terminal.
		a.showPane(inUse.pane)
		return inUse.pane.id, nil
	}
	if err != nil {
		return 0, err
	}
	a.place(c, at)
	return c.id, nil
}

// makeConn builds a pane and registers it. It does not place or start it.
// An AI pane gets no SSH session.
func (a *App) makeConn(hostID string, kind layout.Kind, link int) (*conn, error) {
	if !a.ready() {
		return nil, keychain.ErrLocked
	}
	h, ok := a.host(hostID)
	if !ok {
		return nil, assets.ErrNotFound
	}
	// An assistant opens on any host: on these it types into the terminal.
	if isLocal(h.ID) && kind != layout.Terminal && kind != layout.AI {
		return nil, errLocalTerminalOnly
	}
	if isStream(h) && kind != layout.Terminal && kind != layout.AI {
		return nil, errTerminalOnly
	}
	if kind == layout.Terminal {
		if ex := a.serialPane(h); ex != nil {
			return nil, &serialInUseError{ex}
		}
	}
	a.nextConn++
	c := &conn{id: a.nextConn, kind: kind, host: h}
	if isTemp(h.ID) {
		c.temp = h.ID
	}
	if kind == layout.AI {
		agentName := a.settings.AIDefaultAgent
		if agentName == "" || !hasSSH(h) {
			agentName = "builtin"
		}
		c.ai = &aiState{agent: agentName, link: link, provider: a.defaultProvider()}
		a.conns[c.id] = c
		return c, nil
	}
	opts := session.Options{
		KeepAlive:     time.Duration(a.settings.KeepAliveSeconds) * time.Second,
		AutoReconnect: a.settings.AutoReconnect,
		NoShell:       kind != layout.Terminal,
		Local:         isLocal(h.ID),
		Term:          cmp.Or(h.Term, a.settings.TerminalType),
	}
	var cfg session.Config
	switch {
	case isStream(h):
		cfg = a.streamSession(h, c, &opts)
	case !isLocal(h.ID):
		cfg = a.hostConfig(h, c, func(ctx context.Context) (session.Credentials, error) { return a.credentials(c, h) })
	}
	if opts.Local {
		opts.Shell = a.shell(h.ID)
	}
	if kind == layout.Terminal {
		opts.Dir = h.TerminalDir
		if !opts.Local && !isStream(h) {
			c.start = &shellStart{host: h.Tmux}
			opts.Start = a.shellStartCmd(c)
		}
	}
	c.s = session.New(cfg, opts, 80, 24)
	c.s.OnChange = a.invalidate
	if kind != layout.Terminal {
		c.fs = newFilesState(a.invalidate)
		c.fs.transfers.Client = a.transferClient(c)
		c.fs.transfers.SetConcurrency(a.settings.TransferConcurrency)
	} else {
		if h.Encoding != "" {
			// An encoding this version does not know stays UTF-8.
			_ = c.s.Term.SetOutputEncoding(vt.OutputEncoding(h.Encoding))
		}
		c.tv = newTermView(sessTarget{c.s}, float32(a.settings.TerminalFontSize))
		c.tv.inputError = a.snippetSendResult
		c.tv.family = a.termFamily()
		a.spaceTerm(c.tv)
		a.armOSC52(c)
		if a.termSeq == nil {
			a.termSeq = map[string]int{}
		}
		a.termSeq[h.ID]++
		c.termNo = a.termSeq[h.ID]
	}
	a.conns[c.id] = c
	return c, nil
}

// place shows c in the workspace and starts its session, when it has one.
func (a *App) place(c *conn, at openAt) {
	if at.pane == 0 || !a.ws.Place(c.item(), at.pane, at.edge) {
		a.ws.Open(c.item())
	} else if railPane(c.kind) && (at.edge == layout.Left || at.edge == layout.Right) {
		a.fitRailWidth(c)
	}
	a.shown(c)
}

// shown starts c, now in a pane, and focuses it.
func (a *App) shown(c *conn) {
	a.focusReq = c.id
	a.page = pageWorkspace
	a.startConn(c)
	if !a.restoring {
		a.saveLayout()
	}
}

// startConn starts c's session, once the secrets it needs are unlocked.
func (a *App) startConn(c *conn) {
	if c.temp == "" {
		a.store.Touch(c.host.ID)
	}
	if c.s != nil {
		if a.connectionNeedsSecrets(c.host) {
			c.waitingSecrets = true
			a.withSecrets(func() {
				if a.conns[c.id] == c {
					c.waitingSecrets = false
					c.s.Start()
				}
			})
		} else {
			c.s.Start()
		}
	}
}

// connectionNeedsSecrets checks metadata only; typed and agent credentials
// remain usable without requesting the local master key.
func (a *App) connectionNeedsSecrets(h assets.Host) bool {
	if a.unlocked() || !a.unlock.stored || isLocal(h.ID) {
		return false
	}
	path, _ := a.store.JumpPath(h.ID)
	path = append(path, h)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, hop := range path {
		switch hop.Auth {
		case assets.AuthKey:
			return true
		case assets.AuthCredential:
			cr, ok := a.store.Credential(hop.CredentialID)
			if !ok || cr.Kind != assets.CredPassword || a.typed[keychain.Cred(cr.ID, keychain.Password)] == "" {
				return true
			}
		case assets.AuthPassword:
			if a.typed[keychain.Host(hop.ID, keychain.Password)] == "" {
				return true
			}
		}
		if px, ok := a.store.Proxy(hop.ProxyID); ok && px.User != "" {
			return true
		}
	}
	return false
}

// hostConfig is how to reach h, on its shared connection, logging in
// with creds: through its jump hosts, each logging in with its own
// credentials (typed ones held for c), and the proxy of the first hop.
func (a *App) hostConfig(h assets.Host, c *conn, creds func(context.Context) (session.Credentials, error)) session.Config {
	cfg := session.Config{
		Pool:        a.pool,
		Key:         a.poolKey(h),
		Name:        h.Label(),
		ID:          h.ID,
		Addr:        h.Addr(),
		User:        h.User,
		HostKey:     a.known.Callback(),
		Dial:        a.p.Dial,
		Credentials: creds,
		Ask:         a.challengeAsker(c),
	}
	path, err := a.store.JumpPath(h.ID)
	if err != nil && !errors.Is(err, assets.ErrNotFound) {
		cfg.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, err }
		return cfg
	}
	for _, j := range path {
		cfg.Jumps = append(cfg.Jumps, session.Hop{
			Name: j.Label(), ID: j.ID, Addr: j.Addr(), User: j.User, HostKey: a.known.Callback(),
			Credentials: func(context.Context) (session.Credentials, error) { return a.credentials(c, j) },
		})
	}
	// The proxy is the host's; with none, the first jump host's own, as
	// that is how that host is reached.
	proxyID := h.ProxyID
	if proxyID == "" && len(path) > 0 {
		proxyID = path[0].ProxyID
	}
	if px, ok := a.store.Proxy(proxyID); ok {
		cfg.Proxy = a.proxyDialer(px)
	}
	return cfg
}

// proxyDialer dials through px, its password read from the keychain
// when it is needed.
func (a *App) proxyDialer(px assets.Proxy) *netproxy.Dialer {
	kind := netproxy.SOCKS5
	if px.Kind == assets.ProxyHTTP {
		kind = netproxy.HTTP
	}
	return &netproxy.Dialer{
		Kind: kind, Addr: px.Addr(), User: px.User, Forward: a.p.Dial,
		Password: func(context.Context) (string, error) {
			b, err := a.kc.Get(keychain.ProxyPassword(px.ID))
			if errors.Is(err, keychain.ErrNotFound) {
				return "", nil
			}
			return string(b), err
		},
	}
}

// poolKey is the identity of a host's shared connection: the host and
// every jump host before it, each with who and where it logs in as, how,
// and through which proxy, so that editing any of them connects anew.
func (a *App) poolKey(h assets.Host) string {
	key := a.hopKey(h)
	if len(h.JumpChain) > 0 {
		path, err := a.store.JumpPath(h.ID)
		if err != nil || h.ID == "" {
			// Not saved, or a bad chain: the IDs, as the dial will fail.
			return key + "|" + strings.Join(h.JumpChain, ",")
		}
		for _, j := range path {
			key += "|" + a.hopKey(j)
		}
	}
	return key
}

// hopKey is one hop of poolKey.
func (a *App) hopKey(h assets.Host) string {
	key := h.ID + "|" + h.User + "@" + h.Addr() + "|" + string(h.Auth) + "|" + h.CredentialID
	if cr, ok := a.store.Credential(h.CredentialID); ok && h.Auth == assets.AuthCredential {
		key += ":" + cr.User + ":" + string(cr.Kind)
	}
	if h.Auth == assets.AuthPassword || h.Auth == assets.AuthKey {
		key += "|" + h.Updated.String()
		a.mu.Lock()
		for _, kind := range keychain.Kinds {
			key += ":" + strconv.Itoa(a.typedGen[keychain.Host(h.ID, kind)])
		}
		a.mu.Unlock()
	}
	key += "|" + h.ProxyID
	if p, ok := a.store.Proxy(h.ProxyID); ok {
		key += ":" + string(p.Kind) + ":" + p.User + "@" + net.JoinHostPort(p.Address, strconv.Itoa(p.Port))
	}
	return key
}

// promptKey is where the secret the user types for h goes in a.typed,
// and whether it is a passphrase; "" when nothing typed would help (an
// agent, a key that was refused or is missing).
func (a *App) promptKey(h assets.Host, kind session.Kind) (key string, passphrase bool) {
	switch h.Auth {
	case assets.AuthPassword:
		return keychain.Host(h.ID, keychain.Password), false
	case assets.AuthKey:
		if kind == session.KindPassphrase {
			return keychain.Host(h.ID, keychain.Passphrase), true
		}
	case assets.AuthAsk:
		return "ask/" + h.ID, false
	case assets.AuthCredential:
		cr, ok := a.store.Credential(h.CredentialID)
		if !ok {
			return "", false
		}
		if cr.Kind == assets.CredPassword {
			return keychain.Cred(cr.ID, keychain.Password), false
		}
		if kind == session.KindPassphrase {
			return keychain.Cred(cr.ID, keychain.Passphrase), true
		}
	}
	return "", false
}

// credentials, h's for the connection c (c's host or one of its jump
// hosts), are read off the UI thread: what the user typed this run, then
// the keychain. An agent's keys are never read.
func (a *App) credentials(c *conn, h assets.Host) (session.Credentials, error) {
	typed := func(key string) string {
		a.mu.Lock()
		defer a.mu.Unlock()
		if c.tried == nil {
			c.tried = map[string]int{}
		}
		c.tried[key] = a.typedGen[key]
		return a.typed[key]
	}
	var out session.Credentials
	switch h.Auth {
	case assets.AuthPassword, assets.AuthKey:
		return a.hostCredentials(h, typed)
	case assets.AuthAgent:
		out.Agent = true
	case assets.AuthCredential:
		cr, ok := a.store.Credential(h.CredentialID)
		if !ok {
			return out, session.ErrNoCredentials
		}
		out.User = cr.User
		switch cr.Kind {
		case assets.CredPassword:
			key := keychain.Cred(cr.ID, keychain.Password)
			out.Password = typed(key)
			if out.Password == "" {
				b, err := a.kc.Get(key)
				if err != nil {
					if errors.Is(err, keychain.ErrNotFound) {
						return out, session.ErrNoCredentials
					}
					return out, err
				}
				out.Password = string(b)
			}
		case assets.CredKey:
			key, err := a.kc.Get(keychain.Cred(cr.ID, keychain.PrivateKey))
			if err != nil {
				if errors.Is(err, keychain.ErrNotFound) {
					return out, session.ErrNoCredentials
				}
				return out, err
			}
			out.PrivateKey, out.Certificate = key, []byte(cr.Certificate)
			pk := keychain.Cred(cr.ID, keychain.Passphrase)
			if p := typed(pk); p != "" {
				out.Passphrase = []byte(p)
			} else if !cr.AskPassphrase {
				var err error
				out.Passphrase, err = a.kc.Get(pk)
				if err != nil && !errors.Is(err, keychain.ErrNotFound) {
					return out, err
				}
			}
		}
	default:
		// The password typed for it, else the keys of this computer, and
		// the password is asked when none of them logs in.
		out.PasswordPrompt = true
		out.Password = typed("ask/" + h.ID)
		if out.Password == "" {
			// A Telnet or serial host has no keys to offer.
			if !a.lkOn.Load() || !h.SSH() {
				return out, session.ErrNoCredentials
			}
			a.localKeyCredentials(&out, h, typed)
		}
	}
	return out, nil
}

// localKeyPrompt is where the passphrase typed for the key of this
// computer of KeyID id goes in a.typed.
func localKeyPrompt(id string) string { return "localkey/" + id }

// localKeyCredentials offers h the agent's keys and the key files of this
// computer for it, those that logged in to h last first. An encrypted
// key opens with the passphrase typed this run, else the one remembered;
// typed is nil where nothing is typed (MCP).
func (a *App) localKeyCredentials(out *session.Credentials, h assets.Host, typed func(string) string) {
	f := localkeys.Find(a.homeDir(), a.localUser(), h.Name, h.Address)
	out.TryAgent, out.IdentitiesOnly, out.Prefer = true, f.IdentitiesOnly, a.lk.Last(h.ID)
	a.mu.Lock()
	for id := range a.skipKeys {
		out.Skip = append(out.Skip, id)
	}
	a.mu.Unlock()
	for i := range f.Keys {
		k := &f.Keys[i]
		if !k.Encrypted {
			continue
		}
		id := session.KeyID(k.Public)
		if typed != nil {
			if p := typed(localKeyPrompt(id)); p != "" {
				k.Passphrase = []byte(p)
				continue
			}
		}
		if a.lk.IsRemembered(id) {
			if b, err := a.kc.Get(keychain.LocalKeyPassphrase(id)); err == nil {
				k.Passphrase = b
			}
		}
	}
	out.LocalKeys = f.Keys
	out.OnKey = func(id string) {
		a.lk.SetLast(h.ID, id)
		a.rememberOnLogin(id)
	}
}

// closeSoon closes the pane on the next frame. Closing while the frame
// is still walking the split tree overwrites the parent it is walking.
func (a *App) closeSoon(id int) {
	a.post(func() { a.CloseSession(id) })
}

// CloseSession closes a pane, terminal or files, and its connection.
func (a *App) CloseSession(id int) {
	c := a.conns[id]
	if c == nil {
		return
	}
	if c.kind == layout.AI {
		a.StopAI(id)
		if err := a.saveChat(c); err != nil {
			return
		}
	}
	a.finishCloseSession(c)
}

// finishCloseSession removes a pane after its history has been saved.
func (a *App) finishCloseSession(c *conn) {
	id := c.id
	a.clip.forget(id)
	if a.clipAsk != nil && a.clipAsk.pane == id {
		a.clipAsk = nil
	}
	a.keepRailWidth(c)
	a.ws.Close(id)
	delete(a.conns, id)
	if c.kind == layout.Broadcast {
		// Typing goes to several terminals only while the pane shows.
		a.bc.live = false
	}
	a.releaseTermNo(c)
	a.syncMetrics()
	// Copies go here, not in the goroutine. RemoveAll unlinks the file
	// before the folder, and c.close waits on the session.
	if c.fs != nil {
		c.fs.copies.close()
	}
	if c.ed != nil {
		a.closeEditorWindow(c)
	}
	go c.close()
	a.dropTemp(c.temp)
	if !a.closing {
		a.saveLayout()
	}
}

// releaseTermNo forgets a host's pane numbers once its last terminal
// pane has closed, so the next one is 1 again.
func (a *App) releaseTermNo(c *conn) {
	if c.termNo == 0 {
		return
	}
	for _, o := range a.conns {
		if o.termNo > 0 && o.host.ID == c.host.ID {
			return
		}
	}
	delete(a.termSeq, c.host.ID)
}

// focused is what the active tab's focused pane shows: a session, never
// a tool pane, which works on the sessions.
func (a *App) focused() *conn {
	if c := a.focusedPane(); c != nil && !isTool(c.kind) {
		return c
	}
	return nil
}

// focusedPane is the active tab's focused pane, a tool pane too.
func (a *App) focusedPane() *conn {
	if t := a.ws.ActiveTab(); t != nil {
		return a.conns[t.Focused().ID]
	}
	return nil
}

// hostsBy groups the hosts the sidebar shows, filtered by the search.
func hostsBy(st *assets.Store, query string) (favorites []assets.Host, groups []string, byGroup map[string][]assets.Host) {
	hosts := st.List()
	if query != "" {
		hosts = st.Search(query)
	}
	byGroup = map[string][]assets.Host{}
	for _, h := range hosts {
		if h.Favorite {
			favorites = append(favorites, h)
		}
		byGroup[h.Group] = append(byGroup[h.Group], h)
	}
	// Groups made on their own show while empty, but not in a search,
	// where a group shows for the hosts of its subgroups too.
	found := map[string]bool{}
	for g := range byGroup {
		for ; g != "" && !found[g]; g = assets.GroupParent(g) {
			found[g] = true
		}
	}
	for _, g := range st.Groups() {
		if found[g] || query == "" {
			groups = append(groups, g)
		}
	}
	return
}
