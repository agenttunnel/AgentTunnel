package cli

import (
	"context"
	"fmt"
	"time"
)

// The Master tab: durable conversations with the home-screen orchestrator. The
// list mode picks a conversation; the chat mode reads its transcript and sends.
// master.send can run for as long as the model takes, so it goes to the
// background, and the transcript reloads when the reply lands.

const tuiViewMaster = "master"

// tuiMasterSummary mirrors transport.MasterThreadSummary.
type tuiMasterSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Messages  int       `json:"messages"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// tuiMasterDetail mirrors transport.MasterThreadDetail.
type tuiMasterDetail struct {
	ID       string             `json:"id"`
	Title    string             `json:"title"`
	Messages []tuiMasterMessage `json:"messages"`
}

type tuiMasterMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// tuiMasterReply is the part of orchestrator.MasterResult the chat shows.
type tuiMasterReply struct {
	Reply   string `json:"reply"`
	Warning string `json:"warning"`
	Error   string `json:"error"`
}

type tuiMasterState struct {
	tuiBase
	list     []tuiMasterSummary
	inChat   bool
	chatID   string
	chatName string
	messages []tuiMasterMessage
	// scroll is how many transcript rows the view sits above the newest one.
	scroll int
	// opens numbers the requests that open a chat; the latest one wins, so
	// pressing Enter on a second conversation before the first loads opens
	// the second, not whichever reply is quicker.
	opens int
}

func tuiMasterOf(m *tuiModel) *tuiMasterState {
	return tuiStateOf[tuiMasterState](m, tuiViewMaster)
}

// fetchJob reads the conversation list and, in chat mode, the open transcript in
// the background. The transcript only lands if the same chat is still open.
func (st *tuiMasterState) fetchJob() tuiJob {
	base := &st.tuiBase
	chat := ""
	if st.inChat {
		chat = st.chatID
	}
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var list []tuiMasterSummary
		if err := c.Call(ctx, "master.history.list", nil, &list); err != nil {
			return tuiViewErrJob(base, err)
		}
		var transcript *tuiMasterDetail
		if chat != "" {
			var d tuiMasterDetail
			if err := c.Call(ctx, "master.history.open", map[string]string{"threadId": chat}, &d); err != nil {
				return tuiViewErrJob(base, err)
			}
			transcript = &d
		}
		return func(m *tuiModel) {
			st := tuiMasterOf(m)
			st.list = list
			if transcript != nil && st.inChat && st.chatID == chat {
				st.chatName, st.messages = transcript.Title, transcript.Messages
				return
			}
			st.cursor = tuiMoveCursor(st.cursor, 0, len(st.list))
		}
	}
}

// open switches to the chat for id. The transcript is fetched in the background,
// and the chat only opens if the list is still showing when it lands.
func (st *tuiMasterState) open(m *tuiModel, id string) {
	base := &st.tuiBase
	st.opens++
	opens := st.opens
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var d tuiMasterDetail
		if err := c.Call(ctx, "master.history.open", map[string]string{"threadId": id}, &d); err != nil {
			return tuiViewErrJob(base, err)
		}
		return func(m *tuiModel) {
			st := tuiMasterOf(m)
			if st.opens != opens {
				return // a newer open or a new conversation took over
			}
			st.chatID, st.chatName, st.messages = id, d.Title, d.Messages
			st.inChat, st.scroll = true, 0
		}
	})
}

// newConversation creates an empty durable conversation in the background and
// opens it, then reloads the list.
func (st *tuiMasterState) newConversation(m *tuiModel) {
	base := &st.tuiBase
	st.opens++
	opens := st.opens
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var d tuiMasterDetail
		if err := c.Call(ctx, "master.history.new", map[string]string{"title": ""}, &d); err != nil {
			return tuiViewErrJob(base, err)
		}
		return func(m *tuiModel) {
			st := tuiMasterOf(m)
			if st.opens != opens {
				return // a newer open or a new conversation took over
			}
			st.chatID, st.chatName, st.messages = d.ID, d.Title, nil
			st.inChat, st.scroll = true, 0
			tuiReloadJob(m, base, st.fetchJob())
		}
	})
}

// send posts text to the open conversation. The reply lands in the transcript,
// so the background run only has to report the outcome.
func (st *tuiMasterState) send(text string) {
	if st.chatID == "" {
		st.say("press n to start a conversation first")
		return
	}
	id := st.chatID
	st.run("asking master", func(ctx context.Context, c cliCaller) (string, error) {
		var reply tuiMasterReply
		if err := c.Call(ctx, "master.send", map[string]any{"text": text, "threadId": id}, &reply); err != nil {
			return "", err
		}
		if reply.Error != "" {
			return "", fmt.Errorf("master: %s", reply.Error)
		}
		if reply.Warning != "" {
			return reply.Warning, nil
		}
		return "master replied", nil
	})
}

// transcriptRows lays the conversation out: each message labelled by who wrote
// it, its text wrapped to width.
func (st *tuiMasterState) transcriptRows(width int) []tuiRow {
	if len(st.messages) == 0 {
		return []tuiRow{tuiFaint("no messages yet · type below and press enter")}
	}
	var out []tuiRow
	for _, msg := range st.messages {
		who, ink := "you", tuiInk{fg: tuiColAccent2, bold: true}
		if msg.Role != "user" {
			who, ink = "master", tuiInk{fg: tuiColAccent, bold: true}
		}
		out = append(out, tuiText(who, ink))
		out = append(out, wrapRows(msg.Text, width-2, tuiInk{fg: tuiColText})...)
		out = append(out, nil)
	}
	return out
}

func (st *tuiMasterState) render(cols, rows int) []tuiRow {
	width := cols
	if rows >= 3 && cols >= 6 {
		width = cols - 4
	}
	status := st.status()
	if st.ed.on {
		status = st.ed.row()
	}
	if !st.inChat {
		return st.listRows(cols, rows, status)
	}
	lines := st.transcriptRows(width)
	end := max(len(lines)-st.scroll, 0)
	start := max(end-max(rows-3, 1), 0)
	return tuiViewFrame(cols, rows, "Master · "+usageDash(st.chatName), lines[start:end], -1, status)
}

func (st *tuiMasterState) listRows(cols, rows int, status tuiRow) []tuiRow {
	if len(st.list) == 0 {
		return tuiViewFrame(cols, rows, "Master", []tuiRow{tuiFaint("no conversations yet · press n to start one")}, -1, status)
	}
	out := make([]tuiRow, 0, len(st.list))
	for _, s := range st.list {
		title := s.Title
		if title == "" {
			title = "untitled"
		}
		out = append(out, tuiText(title, tuiInk{fg: tuiColText, bold: true}).
			join(tuiText(fmt.Sprintf("  %d messages", s.Messages), tuiInk{fg: tuiColDim})))
	}
	return tuiViewFrame(cols, rows, "Master", out, st.cursor, status)
}

func (st *tuiMasterState) key(m *tuiModel, k tuiKey) bool {
	st.touch(k)
	if st.inChat {
		return st.chatKey(m, k)
	}
	if st.ed.on {
		return true
	}
	if step := tuiNav(k, 8); step != 0 {
		st.cursor = tuiMoveCursor(st.cursor, step, len(st.list))
		return true
	}
	switch {
	case k.Kind == keyEnter:
		if st.cursor < len(st.list) {
			st.open(m, st.list[st.cursor].ID)
		}
	case k.Kind == keyRune && k.Rune == 'n':
		st.newConversation(m)
	case k.Kind == keyRune && k.Rune == 'd':
		if st.cursor >= len(st.list) {
			return true
		}
		s := st.list[st.cursor]
		if !st.confirm("delete", "press d again to delete \""+usageDash(s.Title)+"\"", k) {
			return true
		}
		st.run("deleting conversation", func(ctx context.Context, c cliCaller) (string, error) {
			err := c.Call(ctx, "master.history.delete", map[string]string{"threadId": s.ID}, nil)
			return "conversation deleted", err
		})
	case k.Kind == keyRune && k.Rune == 'r':
		tuiReloadJob(m, &st.tuiBase, st.fetchJob())
	default:
		return false
	}
	return true
}

// chatKey edits the message and sends it. Typing opens the editor, so every
// printable key is text in chat mode; Esc leaves the chat for the list.
func (st *tuiMasterState) chatKey(m *tuiModel, k tuiKey) bool {
	switch {
	case st.ed.on:
		if st.ed.key(k) {
			text := st.ed.value()
			st.ed.text = nil
			if text != "" {
				st.send(text)
			}
		}
		return true
	case k.Kind == keyEsc:
		st.inChat = false
		tuiReloadJob(m, &st.tuiBase, st.fetchJob())
		return true
	case k.Kind == keyEnter:
		st.ed.open("ask master", "")
		return true
	case k.Kind == keyPgUp:
		st.scroll += 5
		return true
	case k.Kind == keyPgDn:
		st.scroll = max(st.scroll-5, 0)
		return true
	case k.Kind == keyRune:
		st.ed.open("ask master", "")
		st.ed.key(k)
		return true
	}
	return false
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewMaster,
		Title: "Master",
		Order: 10,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiMasterOf(m)
			st.bind(ctx, c)
			// The transcript changes when a reply lands, so the forced reload from
			// the background send is what keeps it current; the ttl covers others.
			tuiLoadJob(m, &st.tuiBase, 5*time.Second, st.fetchJob())
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiMasterOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiMasterOf(m).key(m, k)
		},
		Hints: []tuiHint{
			{"enter", "open / send"}, {"n", "new"}, {"d", "delete"}, {"esc", "back"}, {"pgup/pgdn", "scroll"}, {"tab", "views"},
		},
	})
}
