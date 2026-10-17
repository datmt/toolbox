// Package tui is the Bubble Tea front end.
package tui

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/datmt/datmt-sweep/internal/core"
	"github.com/datmt/datmt-sweep/internal/power"
	"github.com/datmt/datmt-sweep/internal/safety"
)

type mode int

const (
	modeList mode = iota
	modeFilter
	modeConfirm
	modeDone
)

type (
	itemMsg struct {
		cat int
		it  core.Item
	}
	scanDoneMsg struct {
		cat int
		err error
	}
	previewMsg struct {
		cat      int
		id, text string
	}
	dryRunMsg struct {
		text string
		err  error
	}
	sudoDoneMsg struct{ err error }
	doneMsg     struct {
		cat int
		res core.Result
	}
)

// catState is everything the TUI tracks per sidebar category.
type catState struct {
	cat      core.Category
	items    []core.Item
	view     []int // indices into items after filter/sort
	sel      map[string]bool
	cursor   int
	top      int
	scanning bool
	hidden   int // items dropped by the ignore list
	scanErr  error
	ch       chan core.Item
	preview  map[string]string
}

type Model struct {
	ctx    context.Context
	cats   []*catState
	active int
	filter string
	sortBy string // "size" | "name"
	mode   mode
	w, h   int

	removal   core.Removal
	removeCat int
	permanent bool // confirm screen is for a permanent delete
	dryRun    string
	dryErr    error
	statusMsg string

	// Monitor, if set, adds the live power tab as the last sidebar entry.
	Monitor  *power.Monitor
	monLast  power.Reading
	monHist  []float64
	monPause bool

	// Ignore and Audit are optional (nil = no ignore list / no audit log).
	Ignore *safety.Ignore
	Audit  *safety.Audit
}

func New(ctx context.Context, cats []core.Category) *Model {
	m := &Model{ctx: ctx, sortBy: "size"}
	for _, c := range cats {
		m.cats = append(m.cats, &catState{
			cat: c, sel: map[string]bool{}, preview: map[string]string{},
			scanning: true, ch: make(chan core.Item, 64),
		})
	}
	return m
}

func (m *Model) cs() *catState { return m.cats[m.active] }

// onMonitor is true when the power tab (index len(m.cats)) is showing.
func (m *Model) onMonitor() bool { return m.Monitor != nil && m.active == len(m.cats) }

// tabs is the number of sidebar entries.
func (m *Model) tabs() int {
	if m.Monitor != nil {
		return len(m.cats) + 1
	}
	return len(m.cats)
}

func (m *Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	for i, c := range m.cats {
		sc := c.cat.Scanner()
		go func() {
			err := sc.Scan(m.ctx, c.ch)
			c.scanErr = err // written before close, read after close observes it
			close(c.ch)
		}()
		cmds = append(cmds, m.waitItem(i))
	}
	if m.Monitor != nil {
		cmds = append(cmds, m.monSample()) // baseline now, first real reading after one tick
	}
	return tea.Batch(cmds...)
}

func (m *Model) waitItem(i int) tea.Cmd {
	c := m.cats[i]
	return func() tea.Msg {
		it, ok := <-c.ch
		if !ok {
			return scanDoneMsg{cat: i, err: c.scanErr}
		}
		return itemMsg{cat: i, it: it}
	}
}

func (m *Model) rebuild(c *catState) {
	c.view = c.view[:0]
	f := strings.ToLower(m.filter)
	for i, it := range c.items {
		if f != "" && !strings.Contains(strings.ToLower(it.Title+" "+it.Desc+" "+strings.Join(it.Tags, " ")), f) {
			continue
		}
		c.view = append(c.view, i)
	}
	sort.SliceStable(c.view, func(a, b int) bool {
		x, y := c.items[c.view[a]], c.items[c.view[b]]
		if m.sortBy == "name" {
			return x.Title < y.Title
		}
		return x.Size > y.Size
	})
	if c.cursor >= len(c.view) {
		c.cursor = max(0, len(c.view)-1)
	}
}

func (m *Model) rebuildAll() {
	for _, c := range m.cats {
		m.rebuild(c)
	}
}

func (c *catState) current() *core.Item {
	if c.cursor < len(c.view) {
		return &c.items[c.view[c.cursor]]
	}
	return nil
}

func (c *catState) selected() []core.Item {
	var out []core.Item
	for _, it := range c.items {
		if c.sel[it.ID] {
			out = append(out, it)
		}
	}
	return out
}

// auditRecord describes the pending removal in category m.removeCat.
func (m *Model) auditRecord(event string) safety.Record {
	c := m.cats[m.removeCat]
	rec := safety.Record{Event: event, Category: c.cat.Name(), Summary: m.removal.Summary}
	for _, it := range c.selected() {
		rec.Items = append(rec.Items, it.ID)
	}
	return rec
}

// logAudit never blocks a removal: a failed write only shows in the status line.
func (m *Model) logAudit(r safety.Record) {
	if err := m.Audit.Log(r); err != nil {
		m.statusMsg = "audit log: " + err.Error()
	}
}

func (m *Model) previewCmd() tea.Cmd {
	if m.onMonitor() {
		return nil
	}
	ci := m.active
	c := m.cats[ci]
	it := c.current()
	if it == nil {
		return nil
	}
	if _, ok := c.preview[it.ID]; ok {
		return nil
	}
	item := *it
	return func() tea.Msg {
		return previewMsg{cat: ci, id: item.ID, text: c.cat.Info(m.ctx, item)}
	}
}

func (m *Model) dryRunCmd() tea.Cmd {
	c := m.cs()
	items := c.selected()
	perm := m.permanent
	return func() tea.Msg {
		if pr, ok := c.cat.(core.PermanentRemover); ok && perm {
			out, err := pr.DryRunPermanent(m.ctx, items)
			return dryRunMsg{text: out, err: err}
		}
		out, err := c.cat.DryRun(m.ctx, items)
		return dryRunMsg{text: out, err: err}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case itemMsg:
		c := m.cats[msg.cat]
		if m.Ignore.Match(c.cat.Name(), msg.it.ID) {
			c.hidden++
			return m, m.waitItem(msg.cat)
		}
		c.items = append(c.items, msg.it)
		m.rebuild(c)
		return m, tea.Batch(m.waitItem(msg.cat), m.previewCmd())
	case scanDoneMsg:
		c := m.cats[msg.cat]
		c.scanning = false
		c.scanErr = msg.err
	case previewMsg:
		m.cats[msg.cat].preview[msg.id] = msg.text
	case dryRunMsg:
		m.dryRun, m.dryErr = msg.text, msg.err
	case sudoDoneMsg:
		m.mode = modeDone
		rec := m.auditRecord("result")
		if msg.err != nil {
			rec.Error = msg.err.Error()
		} else {
			rec.Done = rec.Items // sudo batch is all-or-nothing
		}
		m.logAudit(rec)
		if msg.err != nil {
			m.statusMsg = "removal failed: " + msg.err.Error()
		} else {
			m.statusMsg = "removal finished"
		}
		return m, tea.Quit
	case doneMsg:
		c := m.cats[msg.cat]
		rec := m.auditRecord("result")
		rec.Done = msg.res.Done
		for id, err := range msg.res.Failed {
			rec.Failed = append(rec.Failed, id+": "+err.Error())
		}
		sort.Strings(rec.Failed)
		m.logAudit(rec)
		gone := map[string]bool{}
		for _, id := range msg.res.Done {
			gone[id] = true
		}
		kept := c.items[:0]
		var freed int64
		for _, it := range c.items {
			if gone[it.ID] {
				freed += it.Size
				delete(c.sel, it.ID)
				continue
			}
			kept = append(kept, it)
		}
		c.items = kept
		m.rebuild(c)
		m.mode = modeList
		m.statusMsg = fmt.Sprintf("removed %d, freed %s", len(msg.res.Done), core.HumanSize(freed))
		for id, err := range msg.res.Failed {
			m.statusMsg += fmt.Sprintf(" · FAILED %s: %v", id, err)
			break
		}
		if n := len(msg.res.Failed); n > 1 {
			m.statusMsg += fmt.Sprintf(" (+%d more failures)", n-1)
		}
		return m, m.previewCmd()
	case monMsg:
		m.monLast = msg.r
		if msg.r.Valid {
			m.monHist = append(m.monHist, watts(msg.r))
			if len(m.monHist) > monHistLen {
				m.monHist = m.monHist[len(m.monHist)-monHistLen:]
			}
		}
		return m, m.monNext()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	var c *catState
	if !m.onMonitor() {
		c = m.cs()
	}
	switch m.mode {
	case modeFilter:
		switch s {
		case "enter", "esc":
			m.mode = modeList
		case "backspace":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
			}
		default:
			if len(k.Runes) > 0 {
				m.filter += string(k.Runes)
			}
		}
		m.rebuildAll()
		return m, m.previewCmd()
	case modeConfirm:
		switch s {
		case "y", "Y":
			if m.dryErr != nil {
				return m, nil
			}
			r := m.removal
			if r.Sudo != nil {
				argv := append([]string{"sudo"}, r.Sudo...)
				return m, tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg {
					return sudoDoneMsg{err}
				})
			}
			ci := m.removeCat
			m.dryRun = "working..."
			return m, func() tea.Msg { return doneMsg{cat: ci, res: r.Do(m.ctx)} }
		case "n", "N", "esc", "q":
			m.mode = modeList
		}
		return m, nil
	}

	m.statusMsg = ""
	if s == "m" && m.Monitor != nil {
		m.active = len(m.cats)
		return m, nil
	}
	if m.onMonitor() {
		if s == "p" {
			m.monPause = !m.monPause
			return m, nil
		}
		switch s {
		case "ctrl+c", "q", "esc", "tab", "right", "l", "shift+tab", "left", "h":
		default:
			return m, nil // list keys do nothing on the monitor
		}
	}
	switch s {
	case "ctrl+c", "q", "esc":
		return m, tea.Quit
	case "tab", "right", "l":
		m.active = (m.active + 1) % m.tabs()
		return m, m.previewCmd()
	case "shift+tab", "left", "h":
		m.active = (m.active + m.tabs() - 1) % m.tabs()
		return m, m.previewCmd()
	case "up", "k":
		c.cursor = max(0, c.cursor-1)
	case "down", "j":
		c.cursor = max(0, min(len(c.view)-1, c.cursor+1))
	case "pgup":
		c.cursor = max(0, c.cursor-10)
	case "pgdown":
		c.cursor = max(0, min(len(c.view)-1, c.cursor+10))
	case "g", "home":
		c.cursor = 0
	case "G", "end":
		c.cursor = max(0, len(c.view)-1)
	case " ":
		if it := c.current(); it != nil {
			c.sel[it.ID] = !c.sel[it.ID]
			if !c.sel[it.ID] {
				delete(c.sel, it.ID)
			}
			c.cursor = max(0, min(len(c.view)-1, c.cursor+1))
		}
	case "a":
		for _, i := range c.view {
			// Danger items are never bulk-selected.
			if c.items[i].Risk != core.Danger {
				c.sel[c.items[i].ID] = true
			}
		}
	case "d":
		c.sel = map[string]bool{}
	case "i":
		if it := c.current(); it != nil {
			if err := m.Ignore.Add(c.cat.Name(), it.ID); err != nil {
				m.statusMsg = "ignore failed: " + err.Error()
				break
			}
			id := it.ID
			kept := c.items[:0]
			for _, x := range c.items {
				if x.ID != id {
					kept = append(kept, x)
				}
			}
			c.items = kept
			delete(c.sel, id)
			c.hidden++
			m.rebuild(c)
			m.statusMsg = "ignored " + id + " (ignore.toml)"
		}
	case "s":
		if m.sortBy == "size" {
			m.sortBy = "name"
		} else {
			m.sortBy = "size"
		}
		m.rebuildAll()
	case "/":
		m.mode = modeFilter
	case "enter", "D":
		perm := k.String() == "D"
		pr, canPerm := c.cat.(core.PermanentRemover)
		if perm && !canPerm {
			m.statusMsg = c.cat.Name() + ": removal is already permanent, use enter"
			return m, nil
		}
		if len(c.sel) == 0 {
			if it := c.current(); it != nil {
				c.sel[it.ID] = true
			}
		}
		items := c.selected()
		if len(items) == 0 {
			return m, nil
		}
		if perm {
			m.removal = pr.RemovePermanent(items)
		} else {
			m.removal = c.cat.Remove(items)
		}
		m.permanent = perm
		m.removeCat = m.active
		m.mode = modeConfirm
		m.dryRun, m.dryErr = "running dry run...", nil
		return m, m.dryRunCmd()
	}
	return m, m.previewCmd()
}

var (
	cursorSt = lipgloss.NewStyle().Reverse(true)
	dimSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	headSt   = lipgloss.NewStyle().Bold(true)
	riskSt   = map[core.Risk]lipgloss.Style{
		core.Safe:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		core.Review: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		core.Danger: lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
)

func (m *Model) View() string {
	if m.w == 0 {
		return "loading..."
	}
	switch m.mode {
	case modeConfirm:
		return m.confirmView()
	case modeDone:
		return m.statusMsg + "\n"
	}
	return m.listView()
}

const sideW = 16

// sidebar renders the category list (plus the monitor entry) into h rows.
func (m *Model) sidebar(h int) []string {
	side := make([]string, h)
	for i, oc := range m.cats {
		if i >= h {
			break
		}
		label := fmt.Sprintf("%s %d", oc.cat.Name(), len(oc.items))
		if oc.scanning {
			label += "…"
		}
		label = trunc(label, sideW-1)
		if i == m.active {
			side[i] = cursorSt.Render(pad(label, sideW))
		} else {
			side[i] = label
		}
	}
	if m.Monitor != nil && len(m.cats) < h {
		label := trunc("⚡ Monitor "+m.monShort(), sideW-1)
		if m.onMonitor() {
			label = cursorSt.Render(pad(label, sideW))
		}
		side[len(m.cats)] = label
	}
	return side
}

func (m *Model) listView() string {
	if m.onMonitor() {
		return m.monitorView()
	}
	c := m.cs()
	bodyH := max(1, m.h-3)
	mainW := m.w - sideW - 1
	leftW := mainW * 55 / 100
	rightW := mainW - leftW - 1

	if c.cursor < c.top {
		c.top = c.cursor
	}
	if c.cursor >= c.top+bodyH {
		c.top = c.cursor - bodyH + 1
	}

	side := m.sidebar(bodyH)

	var left []string
	for row := 0; row < bodyH; row++ {
		i := c.top + row
		if i >= len(c.view) {
			left = append(left, "")
			continue
		}
		it := c.items[c.view[i]]
		mark := "[ ]"
		if c.sel[it.ID] {
			mark = "[x]"
		}
		tags := strings.Join(it.Tags, " ")
		line := fmt.Sprintf("%s %-*s %-12s %10s", mark, max(10, leftW-30), trunc(it.Title, max(10, leftW-30)), tags, sizeCol(it))
		line = trunc(line, leftW)
		if i == c.cursor {
			line = cursorSt.Render(pad(line, leftW))
		} else {
			line = riskSt[it.Risk].Render(line)
		}
		left = append(left, line)
	}

	var right []string
	if it := c.current(); it != nil {
		right = strings.Split(c.preview[it.ID], "\n")
	}
	for i := range right {
		right[i] = trunc(right[i], rightW)
	}
	if len(right) > bodyH {
		right = right[:bodyH]
	}
	for len(right) < bodyH {
		right = append(right, "")
	}

	var b strings.Builder
	state := ""
	if c.scanning {
		state = " scanning..."
	}
	if c.scanErr != nil {
		state = " scan error: " + c.scanErr.Error()
	}
	total := sizeTotal(c.selected())
	b.WriteString(headSt.Render(fmt.Sprintf("datmt-sweep · %s · %d items%s", c.cat.Name(), len(c.view), state)) + "\n")
	for i := 0; i < bodyH; i++ {
		b.WriteString(pad(side[i], sideW) + dimSt.Render("│") + pad(left[i], leftW) + dimSt.Render("│") + right[i] + "\n")
	}
	foot := fmt.Sprintf("selected %d · %s · sort:%s", len(c.sel), total, m.sortBy)
	if c.hidden > 0 {
		foot += fmt.Sprintf(" · %d ignored", c.hidden)
	}
	if m.mode == modeFilter {
		foot = "filter: " + m.filter + "█"
	} else if m.filter != "" {
		foot += " · filter: " + m.filter
	}
	if m.statusMsg != "" {
		foot = m.statusMsg
	}
	b.WriteString(foot + "\n")
	b.WriteString(dimSt.Render("tab category · space select · a all · d none · s sort · / filter · i ignore · enter remove · D delete permanently · q quit"))
	return b.String()
}

func (m *Model) confirmView() string {
	var b strings.Builder
	c := m.cats[m.removeCat]
	sel := c.selected()
	b.WriteString(headSt.Render(fmt.Sprintf("%s: %d items (%s)", c.cat.Name(), len(sel), sizeTotal(sel))) + "\n\n")
	b.WriteString("Dry run:\n")
	if m.dryErr != nil {
		b.WriteString(riskSt[core.Danger].Render("refused: "+m.dryErr.Error()) + "\n")
		b.WriteString("\nNothing will be removed. [esc] back\n")
		return b.String()
	}
	lines := strings.Split(strings.TrimSpace(m.dryRun), "\n")
	maxL := max(1, m.h-8)
	for i, l := range lines {
		if i >= maxL {
			b.WriteString(fmt.Sprintf("  ... %d more\n", len(lines)-maxL))
			break
		}
		b.WriteString("  " + trunc(l, m.w-2) + "\n")
	}
	b.WriteString("\nWill: " + m.removal.Summary + "\n")
	b.WriteString(riskSt[core.Review].Render("Remove? [y/N]") + "\n")
	return b.String()
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// sizeCol is the right-hand column: the item's Metric, else its size.
func sizeCol(it core.Item) string {
	if it.Metric != "" {
		return it.Metric
	}
	return core.HumanSize(it.Size)
}

// sizeTotal summarizes a selection; metric items have no byte total.
func sizeTotal(items []core.Item) string {
	var n int64
	for _, it := range items {
		if it.Metric != "" {
			return "no size estimate"
		}
		n += it.Size
	}
	return core.HumanSize(n) + " reclaimable"
}
