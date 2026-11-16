// Package history keeps the AI conversations of each host, to look back
// on and carry on. A conversation holds what its pane showed and, for the
// built-in agent, the messages it sends the model; both may hold terminal
// output and command results, so every file is sealed by the keychain's
// master key. Each host has a folder: an index of its conversations, the
// newest first, and a file for each.
package history

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/settings"
)

// Sealer seals the store's files; *keychain.Keychain is one.
type Sealer interface {
	Seal(purpose string, plain, aad []byte) ([]byte, error)
	Unseal(purpose string, sealed, aad []byte) ([]byte, error)
}

const purpose = "ai-history/v1"

// MaxPerHost is how many conversations a host keeps: saving one more
// drops the oldest. MaxSize is the most a conversation's file holds; a
// longer one keeps its latest items.
const (
	MaxPerHost = 100
	MaxSize    = 4 << 20
)

// ErrNotFound is a conversation that is not kept.
var ErrNotFound = errors.New("history: conversation not found")

// Conversation is one conversation of a host.
type Conversation struct {
	ID       string    `json:"id"`
	Host     string    `json:"host"`
	Title    string    `json:"title"`
	Agent    string    `json:"agent,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
	Effort   string    `json:"effort,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Items    []Item    `json:"items"`
	// Messages are the built-in agent's, without its system prompt; nil
	// for an external agent, which keeps its own.
	Messages []llm.Message `json:"messages,omitempty"`
	// ACP is the external agent's session that last carried the
	// conversation on, to load again; nil when none did, as in files
	// saved before it was kept.
	ACP *ACPSession `json:"acp,omitempty"`
}

// ACPSession is an external agent's session: its ID, the agent (the
// pane's backend) it belongs to and the sandbox it ran in.
type ACPSession struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Cwd   string `json:"cwd,omitempty"`
}

// Item is one entry of the timeline: a question (user), an answer
// (text), thinking (think), a tool call (tool) or an error (err).
type Item struct {
	Kind  string          `json:"kind"`
	Text  string          `json:"text,omitempty"`
	Files []File          `json:"files,omitempty"`
	Tool  *agent.ToolCard `json:"tool,omitempty"`
}

// File names a file a question had attached; its content is not kept.
type File struct {
	Name string `json:"name"`
	MIME string `json:"mime,omitempty"`
}

// Summary is a conversation as the index lists it.
type Summary struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Agent    string    `json:"agent,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

// NewID is a new conversation's ID.
func NewID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Title is the title of a conversation first asked q: its first line,
// cut to about 40 characters.
func Title(q string) string {
	q, _, _ = strings.Cut(strings.TrimSpace(q), "\n")
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > 40 {
		q = strings.TrimSpace(string(r[:40])) + "…"
	}
	return q
}

// Store is the conversations under a folder.
type Store struct {
	dir string
	s   Sealer
}

// New is the store under dir, sealed by s.
func New(dir string, s Sealer) *Store { return &Store{dir: dir, s: s} }

// hostDir is the folder of host, named by a digest of its ID so that any
// ID makes a name.
func (st *Store) hostDir(host string) string {
	sum := sha256.Sum256([]byte(host))
	return filepath.Join(st.dir, hex.EncodeToString(sum[:12]))
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// read opens the file name of host, bound to them.
func (st *Store) read(host, name string, v any) error {
	b, err := os.ReadFile(filepath.Join(st.hostDir(host), name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	plain, err := st.s.Unseal(purpose, b, []byte(host+"\x00"+name))
	if err != nil {
		return err
	}
	defer clear(plain)
	return json.Unmarshal(plain, v)
}

func (st *Store) write(host, name string, v any) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	defer clear(plain)
	sealed, err := st.s.Seal(purpose, plain, []byte(host+"\x00"+name))
	if err != nil {
		return err
	}
	dir := st.hostDir(host)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return settings.WriteFileAtomic(filepath.Join(dir, name), sealed, 0o600)
}

// List is host's conversations, the latest first.
func (st *Store) List(host string) ([]Summary, error) {
	var out []Summary
	err := st.read(host, "index", &out)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return out, err
}

// Load is host's conversation id.
func (st *Store) Load(host, id string) (Conversation, error) {
	var c Conversation
	if !validID(id) {
		return c, ErrNotFound
	}
	if err := st.read(host, id, &c); err != nil {
		return Conversation{}, err
	}
	if c.ID != id || c.Host != host {
		return Conversation{}, ErrNotFound
	}
	return c, nil
}

// Save keeps c, first in its host's index, and drops the conversations
// past MaxPerHost.
func (st *Store) Save(c Conversation) error {
	if !validID(c.ID) || c.Host == "" {
		return ErrNotFound
	}
	list, err := st.List(c.Host)
	if err != nil {
		return err
	}
	fit(&c)
	if err := st.write(c.Host, c.ID, c); err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(s Summary) bool { return s.ID == c.ID })
	list = append([]Summary{{ID: c.ID, Title: c.Title, Agent: c.Agent, Provider: c.Provider, Model: c.Model, Created: c.Created, Updated: c.Updated}}, list...)
	var gone []Summary
	if len(list) > MaxPerHost {
		list, gone = list[:MaxPerHost], list[MaxPerHost:]
	}
	if err := st.write(c.Host, "index", list); err != nil {
		return err
	}
	for _, s := range gone {
		_ = os.Remove(filepath.Join(st.hostDir(c.Host), s.ID))
	}
	return nil
}

// fit cuts c's oldest items until it is under MaxSize, then its oldest
// messages; a message of a tool call goes with its results.
func fit(c *Conversation) {
	for {
		b, err := json.Marshal(c)
		if err != nil || len(b) <= MaxSize-1024 {
			return
		}
		switch {
		case len(c.Items) > 1:
			c.Items = c.Items[max(len(c.Items)/8, 1):]
		case len(c.Messages) > 1:
			n := max(len(c.Messages)/8, 1)
			for n < len(c.Messages) && c.Messages[n].Role == "tool" {
				n++
			}
			c.Messages = c.Messages[n:]
		default:
			c.Items, c.Messages = nil, nil
			return
		}
	}
}

// Delete forgets host's conversation id.
func (st *Store) Delete(host, id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	list, err := st.List(host)
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(s Summary) bool { return s.ID == id })
	if err := st.write(host, "index", list); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(st.hostDir(host), id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// DeleteHost forgets every conversation of host.
func (st *Store) DeleteHost(host string) error {
	return os.RemoveAll(st.hostDir(host))
}
