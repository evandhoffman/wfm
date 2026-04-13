package main

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/evandhoffman/wfm/wifi"
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

var (
	docStyle    = lipgloss.NewStyle().Margin(1, 2)
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	boldStyle   = lipgloss.NewStyle().Bold(true)

	tableStyles = func() table.Styles {
		s := table.DefaultStyles()
		s.Header = s.Header.
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			BorderBottom(true).
			Bold(true)
		s.Selected = s.Selected.
			Foreground(lipgloss.Color("255")).
			Background(lipgloss.Color("24")).
			Bold(true)
		return s
	}()

	connectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("46")).Bold(true)  // bright green
	savedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))            // dim grey
)

// ansiEscape matches ANSI/VT escape sequences so we can strip them from
// rendered strings before doing character-level width calculations.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// ---------------------------------------------------------------------------
// App state machine
// ---------------------------------------------------------------------------

type appState int

const (
	stateStartup       appState = iota // detecting backend
	stateScanning                      // running Scan()
	stateList                          // showing network table
	stateDetail                        // full info for one network
	statePasswordEntry                 // prompting for WPA passphrase
	stateConnecting                    // running Connect()
	stateError                         // fatal error
)

// ---------------------------------------------------------------------------
// Signal helpers
// ---------------------------------------------------------------------------

// signalBars converts a dBm value to a 4-block unicode bar display.
func signalBars(dbm int) string {
	switch {
	case dbm >= -50:
		return "████"
	case dbm >= -60:
		return "███░"
	case dbm >= -70:
		return "██░░"
	case dbm >= -80:
		return "█░░░"
	default:
		return "░░░░"
	}
}

// signalLabel returns a human-readable signal quality label.
func signalLabel(dbm int) string {
	switch {
	case dbm >= -50:
		return "Very Strong"
	case dbm >= -60:
		return "Strong"
	case dbm >= -70:
		return "Medium"
	case dbm >= -80:
		return "Weak"
	default:
		return "Very Weak"
	}
}

// freqToChannel converts a frequency in MHz to its 802.11 channel number.
func freqToChannel(freq int) int {
	switch {
	case freq == 2484:
		return 14
	case freq >= 2412 && freq < 2484:
		return (freq - 2407) / 5
	case freq >= 5160 && freq <= 5885:
		return (freq - 5000) / 5
	case freq >= 5955:
		return (freq - 5950) / 5
	}
	return 0
}

// freqToBand returns a short band label for a MHz frequency.
func freqToBand(freq int) string {
	switch {
	case freq >= 2400 && freq < 2500:
		return "2.4 GHz"
	case freq >= 5000 && freq < 5900:
		return "  5 GHz"
	case freq >= 5900:
		return "  6 GHz"
	default:
		return "       "
	}
}

// ---------------------------------------------------------------------------
// Table column layout
// ---------------------------------------------------------------------------

// logPaneHeight is the number of log lines shown in the activity pane.
// The pane always occupies exactly logPaneHeight+1 rows (separator + lines)
// so the table height never shifts when log content appears.
const logPaneHeight = 5

// fixedColsRenderedWidth is the total rendered width (content + 2-char padding
// per cell) of every column except SSID.
//
//	Status(1)+pad SSID(dynamic)+pad Bars(4)+pad dBm(7)+pad Band(7)+pad Auth(9)+pad
//	fixed (excluding SSID): 3 + 6 + 9 + 9 + 11 = 38
const fixedColsRenderedWidth = 38

func tableColumns(ssidWidth int) []table.Column {
	return []table.Column{
		{Title: " ", Width: 1}, // connection status indicator
		{Title: "SSID", Width: ssidWidth},
		{Title: "Sig", Width: 4},
		{Title: "dBm", Width: 7},
		{Title: "Band", Width: 7},
		{Title: "Auth", Width: 9},
	}
}

func ssidColumnWidth(termWidth int) int {
	w := termWidth - fixedColsRenderedWidth - 2 // -2 for SSID cell padding
	if w < 12 {
		w = 12
	}
	return w
}

// ---------------------------------------------------------------------------
// Tea messages
// ---------------------------------------------------------------------------

type (
	backendReadyMsg struct {
		backend wifi.Backend
		err     error
	}
	scanResultMsg struct {
		networks []wifi.Network
		status   wifi.ConnectionStatus
		err      error
	}
	connectResultMsg struct{ err error }
	logMsg           struct{ line string }
)

// ---------------------------------------------------------------------------
// Tea commands
// ---------------------------------------------------------------------------

func detectBackendCmd() tea.Msg {
	b, err := wifi.Detect()
	return backendReadyMsg{backend: b, err: err}
}

func scanCmd(b wifi.Backend) tea.Cmd {
	return func() tea.Msg {
		nets, err := b.Scan()
		if err != nil {
			return scanResultMsg{err: err}
		}
		status, _ := b.Status()
		return scanResultMsg{networks: nets, status: status}
	}
}

func connectCmd(b wifi.Backend, ssid, passphrase string) tea.Cmd {
	return func() tea.Msg {
		// passphrase is intentionally not logged anywhere in this call chain
		err := b.Connect(ssid, passphrase)
		return connectResultMsg{err: err}
	}
}

// logCmd creates a command that delivers a timestamped log line to the model.
// The timestamp is captured at call time so ordering is preserved even if
// the runtime delivers the message slightly later.
func logCmd(format string, args ...any) tea.Cmd {
	line := time.Now().Format("15:04:05.00") + "  " + fmt.Sprintf(format, args...)
	return func() tea.Msg { return logMsg{line: line} }
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type model struct {
	state      appState
	backend    wifi.Backend
	networks   []wifi.Network // parallel to table rows
	connStatus wifi.ConnectionStatus
	table      table.Model
	spinner    spinner.Model
	input      textinput.Model
	selected   wifi.Network
	logLines   []string // activity log; capped at 200 entries
	width      int      // current terminal width
	height     int      // current terminal height
	err        error
}

func initialModel() model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	inp := textinput.New()
	inp.Placeholder = "passphrase"
	inp.EchoMode = textinput.EchoPassword
	inp.EchoCharacter = '•'

	t := table.New(
		table.WithColumns(tableColumns(20)),
		table.WithFocused(true),
		table.WithHeight(10),
		table.WithStyles(tableStyles),
	)

	return model{
		state:   stateStartup,
		table:   t,
		spinner: sp,
		input:   inp,
		width:   80,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(detectBackendCmd, m.spinner.Tick, logCmd("detecting WiFi backend…"))
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h, v := docStyle.GetFrameSize()
		w := msg.Width - h
		ht := msg.Height - v
		m.table.SetWidth(w)
		m.table.SetHeight(ht - 3 - (logPaneHeight + 1)) // +1 for separator line
		m.table.SetColumns(tableColumns(ssidColumnWidth(w)))
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case logMsg:
		m.logLines = append(m.logLines, msg.line)
		if len(m.logLines) > 200 {
			m.logLines = m.logLines[len(m.logLines)-200:]
		}
		return m, nil

	case backendReadyMsg:
		if msg.err != nil {
			slog.Error("backend detection failed", "err", msg.err)
			m.state = stateError
			m.err = msg.err
			return m, logCmd("backend detection failed: %v", msg.err)
		}
		m.backend = msg.backend
		m.state = stateScanning
		return m, tea.Batch(logCmd("backend ready, scanning…"), scanCmd(m.backend))

	case scanResultMsg:
		if msg.err != nil {
			slog.Error("scan failed", "err", msg.err)
			m.state = stateError
			m.err = fmt.Errorf("scan failed: %w", msg.err)
			return m, logCmd("scan failed: %v", msg.err)
		}
		m.networks = msg.networks
		m.connStatus = msg.status
		m.table.SetRows(buildRows(msg.networks))
		m.state = stateList
		return m, logCmd("scan complete — %d networks found", len(msg.networks))

	case connectResultMsg:
		if msg.err != nil {
			slog.Error("connect failed", "err", msg.err)
			m.state = stateError
			m.err = fmt.Errorf("connection failed: %w", msg.err)
			return m, logCmd("connection failed: %v", msg.err)
		}
		// Rescan so the table reflects the new connected state.
		m.state = stateScanning
		return m, tea.Batch(logCmd("connected to %q, rescanning…", m.selected.SSID), scanCmd(m.backend))
	}

	return m.updateActiveComponent(msg)
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {

	case stateList:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "r", " ":
			m.state = stateScanning
			return m, tea.Batch(logCmd("rescanning…"), scanCmd(m.backend), m.spinner.Tick)
		case "i":
			cursor := m.table.Cursor()
			if cursor >= 0 && cursor < len(m.networks) {
				m.selected = m.networks[cursor]
				m.state = stateDetail
			}
			return m, nil
		case "enter":
			cursor := m.table.Cursor()
			if cursor >= 0 && cursor < len(m.networks) {
				return m.initiateConnect(m.networks[cursor])
			}
		}

	case stateDetail:
		m.state = stateList
		return m, nil

	case statePasswordEntry:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.input.Reset()
			m.state = stateList
			return m, nil
		case "enter":
			pass := m.input.Value()
			m.input.Reset()
			m.state = stateConnecting
			return m, tea.Batch(
				logCmd("connecting to %q…", m.selected.SSID),
				connectCmd(m.backend, m.selected.SSID, pass),
				m.spinner.Tick,
			)
		}

	case stateError:
		switch msg.String() {
		case "ctrl+c", "q", "esc", "enter":
			return m, tea.Quit
		}

	default:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}

	return m.updateActiveComponent(msg)
}

// initiateConnect starts a connect flow for the selected network.
func (m model) initiateConnect(n wifi.Network) (tea.Model, tea.Cmd) {
	m.selected = n
	if !n.Secured || n.Known {
		slog.Info("connecting without passphrase", "ssid", n.SSID, "known", n.Known)
		m.state = stateConnecting
		return m, tea.Batch(logCmd("connecting to %q…", n.SSID), connectCmd(m.backend, n.SSID, ""), m.spinner.Tick)
	}
	m.state = statePasswordEntry
	m.input.Placeholder = fmt.Sprintf("passphrase for %q", n.SSID)
	m.input.Focus()
	return m, textinput.Blink
}

// updateActiveComponent passes messages to whichever component currently owns input.
func (m model) updateActiveComponent(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.state {
	case stateList:
		m.table, cmd = m.table.Update(msg)
	case statePasswordEntry:
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
}

// connectionBar returns a one-line summary of the current WiFi connection,
// or an empty string when not connected.
func (m model) connectionBar() string {
	cs := m.connStatus
	if !cs.Connected {
		return subtleStyle.Render("Not connected")
	}
	parts := []string{connectedStyle.Render("● " + cs.SSID)}
	if cs.IPAddress != "" {
		parts = append(parts, "IP: "+cs.IPAddress)
	} else {
		parts = append(parts, subtleStyle.Render("(no IPv4)"))
	}
	if cs.Gateway != "" {
		parts = append(parts, "GW: "+cs.Gateway)
	}
	if cs.DNS != "" {
		parts = append(parts, "DNS: "+cs.DNS)
	}
	sep := subtleStyle.Render("  ·  ")
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}

// detailView renders a full-detail screen for m.selected.
func (m model) detailView() string {
	n := m.selected
	var status string
	switch {
	case n.Connected:
		status = connectedStyle.Render("connected")
	case n.Known:
		status = savedStyle.Render("saved")
	default:
		status = "not saved"
	}

	secured := "open"
	if n.Secured {
		secured = "yes"
	}

	ch := freqToChannel(n.Frequency)
	chStr := "unknown"
	if ch > 0 {
		chStr = fmt.Sprintf("%d", ch)
	}

	freqStr := "unknown"
	if n.Frequency > 0 {
		freqStr = fmt.Sprintf("%d MHz", n.Frequency)
	}

	bssid := n.BSSID
	if bssid == "" {
		bssid = "unknown"
	}

	auth := n.AuthType
	if auth == "" {
		auth = "unknown"
	}

	lines := []string{
		boldStyle.Render(n.SSID),
		"",
	}

	// Radio
	lines = append(lines, fmt.Sprintf("  %-14s %s  %d dBm  %s", "Signal:", signalBars(n.Signal), n.Signal, signalLabel(n.Signal)))
	if n.Standard != "" {
		lines = append(lines, fmt.Sprintf("  %-14s %s", "Standard:", n.Standard))
	}
	lines = append(lines, fmt.Sprintf("  %-14s %s  (%s)", "Frequency:", freqStr, freqToBand(n.Frequency)))
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Channel:", chStr))
	if n.ChanWidth > 0 {
		lines = append(lines, fmt.Sprintf("  %-14s %d MHz", "Chan Width:", n.ChanWidth))
	}
	if n.APCount > 0 {
		lines = append(lines, fmt.Sprintf("  %-14s %d", "Access Points:", n.APCount))
	}

	lines = append(lines, "")

	// Security
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Auth:", auth))
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Secured:", secured))
	lines = append(lines, fmt.Sprintf("  %-14s %s", "BSSID:", bssid))

	lines = append(lines, "")

	// Connection
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Status:", status))
	if n.Connected && m.connStatus.LinkSpeed != "" {
		lines = append(lines, fmt.Sprintf("  %-14s %s", "Link Speed:", m.connStatus.LinkSpeed))
	}

	lines = append(lines,
		"",
		subtleStyle.Render("press any key to go back"),
	)

	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// logPaneView renders the activity log pane. It always returns exactly
// logPaneHeight+1 lines (separator + logPaneHeight rows) so the surrounding
// layout never shifts regardless of how many log entries exist.
func (m model) logPaneView(contentWidth int) string {
	sep := subtleStyle.Render(strings.Repeat("─", contentWidth))

	start := len(m.logLines) - logPaneHeight
	if start < 0 {
		start = 0
	}
	visible := m.logLines[start:]

	parts := make([]string, 0, 1+logPaneHeight)
	parts = append(parts, sep)
	for _, l := range visible {
		parts = append(parts, subtleStyle.Render("  "+l))
	}
	for i := len(visible); i < logPaneHeight; i++ {
		parts = append(parts, "") // empty padding lines to keep height fixed
	}
	return strings.Join(parts, "\n")
}

// buildRows converts a slice of Networks into table rows.
func buildRows(networks []wifi.Network) []table.Row {
	rows := make([]table.Row, len(networks))
	for i, n := range networks {
		// Status indicator column: plain ASCII only — lipgloss inside table
		// cells breaks column width calculations.
		var status string
		switch {
		case n.Connected:
			status = "*"
		case n.Known:
			status = "+"
		default:
			status = " "
		}

		// SSID with [connected]/[saved] suffix for no-colour clarity.
		ssid := n.SSID
		if n.Connected {
			ssid += " [connected]"
		} else if n.Known {
			ssid += " [saved]"
		}

		rows[i] = table.Row{
			status,
			ssid,
			signalBars(n.Signal),
			fmt.Sprintf("%d dBm", n.Signal),
			freqToBand(n.Frequency),
			n.AuthType,
		}
	}
	return rows
}

// rescanOverlayView renders the existing network list faded out with a
// centred "Rescanning…" dialog stamped on top.
func (m model) rescanOverlayView() string {
	// Build the background: blurred table + connection bar + footer.
	t := m.table
	t.Blur()
	h, v := docStyle.GetFrameSize()
	contentW := m.width - h
	contentH := m.height - v

	connBar := m.connectionBar()
	footer := subtleStyle.Render("↑/↓: navigate • enter: connect • i: info • r/space: rescan • q: quit")
	logPane := m.logPaneView(contentW)
	bg := t.View() + "\n" + logPane + "\n" + connBar + "\n" + footer

	// Strip ANSI from every background line, pad to contentW, apply faint.
	faint := lipgloss.NewStyle().Faint(true)
	bgLines := strings.Split(bg, "\n")
	for i, line := range bgLines {
		plain := stripANSI(line)
		vis := lipgloss.Width(plain)
		if vis < contentW {
			plain += strings.Repeat(" ", contentW-vis)
		}
		bgLines[i] = faint.Render(plain)
	}
	// Pad to fill the terminal vertically so the dialog always centres cleanly.
	for len(bgLines) < contentH {
		bgLines = append(bgLines, faint.Render(strings.Repeat(" ", contentW)))
	}
	if len(bgLines) > contentH {
		bgLines = bgLines[:contentH]
	}

	// Render the dialog.
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 4).
		Render(fmt.Sprintf("%s Rescanning…", m.spinner.View()))
	dialogLines := strings.Split(dialog, "\n")
	dialogH := len(dialogLines)
	dialogW := lipgloss.Width(dialog)

	// Centre position within the content area.
	startRow := (contentH - dialogH) / 2
	startCol := (contentW - dialogW) / 2
	if startCol < 0 {
		startCol = 0
	}

	// Stamp dialog lines over the faint background lines.
	for i, dl := range dialogLines {
		row := startRow + i
		if row < 0 || row >= len(bgLines) {
			continue
		}
		left := faint.Render(strings.Repeat(" ", startCol))
		right := faint.Render(strings.Repeat(" ", max(0, contentW-startCol-dialogW)))
		bgLines[row] = left + dl + right
	}

	return docStyle.Render(strings.Join(bgLines, "\n"))
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m model) View() string {
	switch m.state {
	case stateStartup:
		return docStyle.Render(fmt.Sprintf("%s Detecting WiFi backend…", m.spinner.View()))

	case stateScanning:
		if len(m.networks) == 0 {
			// Initial scan — no table data yet, just show the spinner.
			return docStyle.Render(fmt.Sprintf("%s Scanning for networks…", m.spinner.View()))
		}
		return m.rescanOverlayView()

	case stateList:
		h, _ := docStyle.GetFrameSize()
		connBar := m.connectionBar()
		footer := subtleStyle.Render("↑/↓: navigate • enter: connect • i: info • r/space: rescan • q: quit")
		logPane := m.logPaneView(m.width - h)
		return docStyle.Render(m.table.View() + "\n" + logPane + "\n" + connBar + "\n" + footer)

	case stateDetail:
		return docStyle.Render(m.detailView())

	case statePasswordEntry:
		return docStyle.Render(fmt.Sprintf(
			"Connect to %s\n\n%s\n\n%s",
			boldStyle.Render(m.selected.SSID),
			m.input.View(),
			subtleStyle.Render("enter: connect • esc: cancel"),
		))

	case stateConnecting:
		h, _ := docStyle.GetFrameSize()
		logPane := m.logPaneView(m.width - h)
		return docStyle.Render(fmt.Sprintf(
			"%s Connecting to %s…\n\n%s",
			m.spinner.View(),
			boldStyle.Render(m.selected.SSID),
			logPane,
		))

	case stateError:
		h, _ := docStyle.GetFrameSize()
		logPane := m.logPaneView(m.width - h)
		return docStyle.Render(fmt.Sprintf(
			"%s\n\n%s\n\n%s\n\n%s",
			errorStyle.Render("Error"),
			m.err.Error(),
			logPane,
			subtleStyle.Render("press any key to exit"),
		))
	}
	return ""
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr, "wfm requires root privileges to manage WiFi.\nPlease re-run with: sudo wfm")
		os.Exit(1)
	}

	// Log to a file so diagnostic output doesn't corrupt the TUI.
	if f, err := os.OpenFile("/tmp/wfm.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
		defer f.Close()
	}

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
