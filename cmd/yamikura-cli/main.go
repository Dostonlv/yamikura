package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#25A065")).
			Padding(0, 1)

	promptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFDF5"))

	commandStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#2D9EE0"))

	responseStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#04B575"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF5370"))
)

type model struct {
	textInput textinput.Model
	viewport  viewport.Model
	history   []historyItem
	client    *RedisClient
	status    string
	width     int
	height    int
}

type historyItem struct {
	command  string
	response string
	isError  bool
}

func initialModel(host string, port int, useTLS bool) model {
	ti := textinput.New()
	ti.Placeholder = "Enter command"
	ti.Focus()
	ti.Width = 80

	vp := viewport.New(80, 20)
	vp.SetContent("")

	client := NewRedisClient(fmt.Sprintf("%s:%d", host, port), useTLS)

	return model{
		textInput: ti,
		viewport:  vp,
		history:   []historyItem{},
		client:    client,
		status:    "Ready",
	}
}

type RedisClient struct {
	addr   string
	conn   net.Conn
	useTLS bool
}

func NewRedisClient(addr string, useTLS bool) *RedisClient {
	return &RedisClient{
		addr:   addr,
		useTLS: useTLS,
	}
}

func (c *RedisClient) Connect() error {
	var err error
	if c.useTLS {
		config := &tls.Config{
			InsecureSkipVerify: true,
		}
		c.conn, err = tls.Dial("tcp", c.addr, config)
	} else {
		c.conn, err = net.Dial("tcp", c.addr)
	}
	return err
}

func parseCommandLine(cmd string) []string {
	var args []string
	var currentArg strings.Builder
	inQuotes := false
	quoteChar := rune(0)

	for _, r := range cmd {
		switch {
		case (r == '"' || r == '\'') && (quoteChar == r || quoteChar == 0):
			if inQuotes {
				quoteChar = 0
			} else {
				quoteChar = r
			}
			inQuotes = !inQuotes
		case r == ' ' && !inQuotes:
			if currentArg.Len() > 0 {
				args = append(args, currentArg.String())
				currentArg.Reset()
			}
		default:
			currentArg.WriteRune(r)
		}
	}

	if currentArg.Len() > 0 {
		args = append(args, currentArg.String())
	}

	return args
}

func (c *RedisClient) ExecuteCommand(cmd string) (string, bool) {
	if c.conn == nil {
		err := c.Connect()
		if err != nil {
			return fmt.Sprintf("Error connecting to %s: %v", c.addr, err), true
		}
	}

	args := parseCommandLine(cmd)
	if len(args) == 0 {
		return "", false
	}

	writer := bufio.NewWriter(c.conn)
	fmt.Fprintf(writer, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(writer, "$%d\r\n%s\r\n", len(arg), arg)
	}
	writer.Flush()

	reader := bufio.NewReader(c.conn)
	resp, err := readResponse(reader)
	if err != nil {
		return fmt.Sprintf("Error reading response: %v", err), true
	}

	return resp, false
}

func readResponse(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	if len(line) == 0 {
		return "", fmt.Errorf("empty response")
	}

	switch line[0] {
	case '+': 
		return strings.TrimSpace(line[1:]), nil
	case '-':
		return "ERROR: " + strings.TrimSpace(line[1:]), nil
	case ':':
		return strings.TrimSpace(line[1:]), nil
	case '$': 
		length, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil {
			return "", err
		}
		if length < 0 {
			return "(nil)", nil
		}
		data := make([]byte, length)
		_, err = io.ReadFull(reader, data)
		if err != nil {
			return "", err
		}
		reader.ReadString('\n')
		return string(data), nil
	case '*':
		count, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil {
			return "", err
		}
		if count < 0 {
			return "(nil)", nil
		}
		var result strings.Builder
		for i := 0; i < count; i++ {
			item, err := readResponse(reader)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&result, "%d) %s\n", i+1, item)
		}
		return result.String(), nil
	default:
		return "", fmt.Errorf("unknown response type: %c", line[0])
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			command := m.textInput.Value()
			if command == "exit" || command == "quit" {
				return m, tea.Quit
			}

			response, isError := m.client.ExecuteCommand(command)

			m.history = append(m.history, historyItem{
				command:  command,
				response: response,
				isError:  isError,
			})

			m.viewport.SetContent(m.historyView())
			m.viewport.GotoBottom()

			m.textInput.Reset()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		m.textInput.Width = m.width - 4
		m.viewport.Width = m.width - 4
		m.viewport.Height = m.height - 6
	}

	m.textInput, cmd = m.textInput.Update(msg)
	cmds = append(cmds, cmd)

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	return fmt.Sprintf(
		"%s\n%s\n\n%s",
		titleStyle.Render(" Yamikura CLI - "+m.client.addr+" "),
		m.viewport.View(),
		promptStyle.Render("> ")+m.textInput.View(),
	)
}

func (m model) historyView() string {
	var sb strings.Builder

	for _, item := range m.history {
		sb.WriteString(commandStyle.Render("> "+item.command) + "\n")

		if item.isError {
			sb.WriteString(errorStyle.Render(item.response) + "\n\n")
		} else {
			sb.WriteString(responseStyle.Render(item.response) + "\n\n")
		}
	}

	return sb.String()
}

func main() {
	host := flag.String("h", "localhost", "Server hostname")
	port := flag.Int("p", 6379, "Server port")
	useTLS := flag.Bool("tls", false, "Use TLS connection")
	execute := flag.String("execute", "", "Execute single command")

	flag.Parse()

	if *execute != "" {
		client := NewRedisClient(fmt.Sprintf("%s:%d", *host, *port), *useTLS)
		if err := client.Connect(); err != nil {
			fmt.Printf("Error connecting: %v\n", err)
			return
		}

		response, isError := client.ExecuteCommand(*execute)
		if isError {
			fmt.Printf("Error: %s\n", response)
		} else {
			fmt.Println(response)
		}
		return
	}

	p := tea.NewProgram(initialModel(*host, *port, *useTLS))
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v\n", err)
	}
}
