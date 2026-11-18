package app

import (
	"errors"
	"reflect"
	"strings"

	"github.com/Licoy/sshyard/internal/ai/policy"
)

var errAIStopUnconfirmed = errors.New("ai: could not confirm the previous turn stopped")

type aiSteer struct {
	input                        string
	files                        []*aiAttach
	gen, turn, link              int
	conv, agent, provider, model string
	effort                       string
}

// steerAI submits the composer's current draft when a turn is still running.
// The next turn starts from onAIDone, after the old runner has cleaned up.
func (a *App) steerAI(c *conn) {
	s := c.ai
	if a.conns[c.id] != c || !s.running || s.cancel == nil || s.interrupting ||
		(strings.TrimSpace(s.input) == "" && len(s.attach) == 0) {
		return
	}
	s.steer = &aiSteer{input: s.input, files: cloneAIAttach(s.attach), gen: s.gen, turn: s.turn, link: s.link,
		conv: s.conv, agent: s.agent, provider: s.provider, model: s.model, effort: s.effort}
	s.interrupting = true
	s.attachGen++
	s.cancel()
	a.answerAI(c, policy.Cancelled)
}

func (a *App) revokeAISteer(c *conn) {
	if c.ai.steer != nil {
		c.ai.steer = nil
		c.ai.gen++
	}
}

func (st *aiSteer) matches(c *conn) bool {
	s := c.ai
	return st != nil && st.gen == s.gen && st.turn == s.turn && st.link == s.link && st.conv == s.conv &&
		st.agent == s.agent && st.provider == s.provider && st.model == s.model && st.effort == s.effort &&
		st.input == s.input && reflect.DeepEqual(st.files, s.attach)
}

func (s *aiState) appendOutput(kind, text string) {
	s.items = appendAIChunk(s.items, s.turnStart, aiItem{kind: kind, text: text, hidden: s.interrupting})
}

// finishInterruption removes the unfinished reply, or reveals held output if
// the model had already completed naturally when the draft was submitted.
func (s *aiState) finishInterruption(discard bool) {
	s.interrupting = false
	old := s.items[s.turnStart:]
	items := old[:0]
	for _, it := range old {
		if discard {
			var keep bool
			it, keep = interruptedAIItem(it)
			if !keep {
				continue
			}
		}
		if it.hidden {
			items = appendChunk(items, it.kind, it.text)
			continue
		}
		items = append(items, it)
	}
	clear(old[len(items):])
	s.items = s.items[:s.turnStart+len(items)]
	if discard {
		s.items = append(s.items, aiItem{kind: "interrupted"})
	}
}

func interruptedAIItem(it aiItem) (aiItem, bool) {
	if it.kind == "tool" && it.tool.Phase != "done" {
		it.tool.Phase, it.tool.Status, it.tool.Exit = "done", "cancel", -1
	}
	return it, it.kind != "text" && it.kind != "think"
}
