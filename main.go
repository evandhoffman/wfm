package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

var docStyle = lipgloss.NewStyle().Margin(1, 2)

// network represents a scanned WiFi network.
type network struct {
	ssid    string
	signal  int    // dBm
	secured bool
}

func (n network) Title() string {
	lock := " "
	if n.secured {
		lock = ""
	}
	return fmt.Sprintf("%s %s", lock, n.ssid)
}

func (n network) Description() string {
	return fmt.Sprintf("Signal: %d dBm", n.signal)
}

func (n network) FilterValue() string { return n.ssid }

type model struct {
	list list.Model
}

func initialModel() model {
	// Placeholder networks — real scanning will replace this.
	items := []list.Item{
		network{ssid: "MyHomeNetwork", signal: -45, secured: true},
		network{ssid: "NeighborWifi", signal: -72, secured: true},
		network{ssid: "CoffeeShop_Free", signal: -60, secured: false},
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Available Networks"

	return model{list: l}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		h, v := docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) View() string {
	return docStyle.Render(m.list.View())
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
