package main

import (
	"fmt"
	"log/slog"
	"os"

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

// ---------------------------------------------------------------------------
// App state machine
// ---------------------------------------------------------------------------

type appState int

const (
	stateStartup       appState = iota // detecting backend
	stateScanning                      // running Scan()
	stateList                          // showing network table
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

// fixedColsRenderedWidth is the total rendered width (content + 2-char padding
// per cell) of every column except SSID.
//
//	Status(1)+pad SSID(dynamic)+pad Bars(4)+pad dBm(7)+pad Quality(11)+pad Band(7)+pad Ch(3)+pad Auth(9)+pad BSSID(17)+pad
//	fixed (excluding SSID): 3 + 6 + 9 + 13 + 9 + 5 + 11 + 19 = 75
const fixedColsRenderedWidth = 75

func tableColumns(ssidWidth int) []table.Column {
	return []table.Column{
		{Title: " ", Width: 1},         // connection status indicator
		{Title: "SSID", Width: ssidWidth},
		{Title: "Sig", Width: 4},
		{Title: "dBm", Width: 7},
		{Title: "Quality", Width: 11},
		{Title: "Band", Width: 7},
		{Title: "Ch", Width: 3},
		{Title: "Auth", Width: 9},
		{Title: "BSSID", Width: 17},
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
	width      int // current terminal width (for column resizing)
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
	return tea.Batch(detectBackendCmd, m.spinner.Tick)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		h, v := docStyle.GetFrameSize()
		w := msg.Width - h
		ht := msg.Height - v
		m.table.SetWidth(w)
		m.table.SetHeight(ht - 3) // leave room for header border + footer line
		m.table.SetColumns(tableColumns(ssidColumnWidth(w)))
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case backendReadyMsg:
		if msg.err != nil {
			slog.Error("backend detection failed", "err", msg.err)
			m.state = stateError
			m.err = msg.err
			return m, nil
		}
		m.backend = msg.backend
		m.state = stateScanning
		return m, scanCmd(m.backend)

	case scanResultMsg:
		if msg.err != nil {
			slog.Error("scan failed", "err", msg.err)
			m.state = stateError
			m.err = fmt.Errorf("scan failed: %w", msg.err)
			return m, nil
		}
		m.networks = msg.networks
		m.connStatus = msg.status
		m.table.SetRows(buildRows(msg.networks))
		m.state = stateList
		return m, nil

	case connectResultMsg:
		if msg.err != nil {
			slog.Error("connect failed", "err", msg.err)
			m.state = stateError
			m.err = fmt.Errorf("connection failed: %w", msg.err)
			return m, nil
		}
		// Rescan so the table reflects the new connected state.
		m.state = stateScanning
		return m, scanCmd(m.backend)
	}

	return m.updateActiveComponent(msg)
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {

	case stateList:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "r":
			m.state = stateScanning
			return m, tea.Batch(scanCmd(m.backend), m.spinner.Tick)
		case "enter":
			cursor := m.table.Cursor()
			if cursor >= 0 && cursor < len(m.networks) {
				return m.initiateConnect(m.networks[cursor])
			}
		}

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
		return m, tea.Batch(connectCmd(m.backend, n.SSID, ""), m.spinner.Tick)
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

// buildRows converts a slice of Networks into table rows.
func buildRows(networks []wifi.Network) []table.Row {
	rows := make([]table.Row, len(networks))
	for i, n := range networks {
		// Status indicator column: ● connected, ○ saved, space otherwise.
		var status string
		switch {
		case n.Connected:
			status = connectedStyle.Render("●")
		case n.Known:
			status = savedStyle.Render("○")
		default:
			status = " "
		}

		// SSID with [connected]/[saved] suffix for screen-reader / no-colour clarity.
		ssid := n.SSID
		if n.Connected {
			ssid += " [connected]"
		} else if n.Known {
			ssid += " [saved]"
		}

		chStr := ""
		if n.Frequency > 0 {
			if ch := freqToChannel(n.Frequency); ch > 0 {
				chStr = fmt.Sprintf("%d", ch)
			}
		}
		rows[i] = table.Row{
			status,
			ssid,
			signalBars(n.Signal),
			fmt.Sprintf("%d dBm", n.Signal),
			signalLabel(n.Signal),
			freqToBand(n.Frequency),
			chStr,
			n.AuthType,
			n.BSSID,
		}
	}
	return rows
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m model) View() string {
	switch m.state {
	case stateStartup:
		return docStyle.Render(fmt.Sprintf("%s Detecting WiFi backend…", m.spinner.View()))

	case stateScanning:
		return docStyle.Render(fmt.Sprintf("%s Scanning for networks…", m.spinner.View()))

	case stateList:
		connBar := m.connectionBar()
		footer := subtleStyle.Render("↑/↓: navigate • enter: connect • r: rescan • q: quit")
		return docStyle.Render(m.table.View() + "\n" + connBar + "\n" + footer)

	case statePasswordEntry:
		return docStyle.Render(fmt.Sprintf(
			"Connect to %s\n\n%s\n\n%s",
			boldStyle.Render(m.selected.SSID),
			m.input.View(),
			subtleStyle.Render("enter: connect • esc: cancel"),
		))

	case stateConnecting:
		return docStyle.Render(fmt.Sprintf(
			"%s Connecting to %s…",
			m.spinner.View(),
			boldStyle.Render(m.selected.SSID),
		))

	case stateError:
		return docStyle.Render(fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			errorStyle.Render("Error"),
			m.err.Error(),
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
