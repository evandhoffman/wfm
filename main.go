package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
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
)

// ---------------------------------------------------------------------------
// App state machine
// ---------------------------------------------------------------------------

type appState int

const (
	stateStartup       appState = iota // detecting backend
	stateScanning                      // running Scan()
	stateList                          // showing network list
	statePasswordEntry                 // prompting for WPA passphrase
	stateConnecting                    // running Connect()
	stateError                         // fatal error
)

// ---------------------------------------------------------------------------
// List item
// ---------------------------------------------------------------------------

type networkItem struct{ n wifi.Network }

func (i networkItem) Title() string {
	lock := "  " // open network
	if i.n.Secured {
		lock = "  " // locked
	}
	var tags string
	if i.n.Connected {
		tags += " [connected]"
	} else if i.n.Known {
		tags += " [saved]"
	}
	return fmt.Sprintf("%s%s%s", lock, i.n.SSID, tags)
}

func (i networkItem) Description() string {
	return fmt.Sprintf("Signal: %s  %d dBm", signalBars(i.n.Signal), i.n.Signal)
}

func (i networkItem) FilterValue() string { return i.n.SSID }

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
		return scanResultMsg{networks: nets, err: err}
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
	state    appState
	backend  wifi.Backend
	list     list.Model
	spinner  spinner.Model
	input    textinput.Model
	selected wifi.Network
	err      error
}

func initialModel() model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	inp := textinput.New()
	inp.Placeholder = "passphrase"
	inp.EchoMode = textinput.EchoPassword
	inp.EchoCharacter = '•'

	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Available Networks"

	return model{
		state:   stateStartup,
		list:    l,
		spinner: sp,
		input:   inp,
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
		h, v := docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
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
		items := make([]list.Item, len(msg.networks))
		for i, n := range msg.networks {
			items[i] = networkItem{n: n}
		}
		m.list.SetItems(items)
		m.state = stateList
		return m, nil

	case connectResultMsg:
		if msg.err != nil {
			slog.Error("connect failed", "err", msg.err)
			m.state = stateError
			m.err = fmt.Errorf("connection failed: %w", msg.err)
			return m, nil
		}
		// Rescan so the list reflects the new connected state.
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
			if item, ok := m.list.SelectedItem().(networkItem); ok {
				return m.initiateConnect(item.n)
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
		// Any key exits; the error is unrecoverable in this session.
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
		m.list, cmd = m.list.Update(msg)
	case statePasswordEntry:
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
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
		footer := subtleStyle.Render("enter: connect • r: rescan • q: quit")
		return docStyle.Render(m.list.View() + "\n" + footer)

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
