package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	ColorReset   = "\033[0m"
	ColorRed     = "\033[31m"
	ColorGreen   = "\033[32m"
	ColorYellow  = "\033[33m"
	ColorBlue    = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorCyan    = "\033[36m"
	ColorGray    = "\033[90m"
)

type Peer struct {
	Address string
	Name    string
}

var PEERS = []Peer{
	{Address: "ip:port", Name: "Bonsai-1"},
}

type ClientPeer struct {
	peer        Peer
	controlConn net.Conn
	connected   bool
	mu          sync.Mutex
}

type Client struct {
	peers          []Peer
	localAddr      string
	subdomain      string
	authKey        string
	sslMode        bool
	reconnectDelay time.Duration
	peerClients    []*ClientPeer
	wg             sync.WaitGroup
}

func NewClient(peers []Peer, localAddr, subdomain, authKey string, sslMode bool) *Client {
	return &Client{
		peers:          peers,
		localAddr:      localAddr,
		subdomain:      subdomain,
		authKey:        authKey,
		sslMode:        sslMode,
		reconnectDelay: 5 * time.Second,
		peerClients:    make([]*ClientPeer, len(peers)),
	}
}

func printBanner() {
	fmt.Println()
	fmt.Printf("%s╔══════════════════════════════════════╗%s\n", ColorCyan, ColorReset)
	fmt.Printf("%s║             BONSAI TUNNEL            ║%s\n", ColorCyan, ColorReset)
	fmt.Printf("%s║           powered by EvaExit         ║%s\n", ColorGray, ColorReset)
	fmt.Printf("%s╚══════════════════════════════════════╝%s\n", ColorCyan, ColorReset)
	fmt.Println()
}

func askQuestion(prompt, defaultValue string) string {
	reader := bufio.NewReader(os.Stdin)
	if defaultValue != "" {
		fmt.Printf("%s%s%s [%s%s%s]: ", ColorYellow, prompt, ColorReset, ColorGray, defaultValue, ColorReset)
	} else {
		fmt.Printf("%s%s%s: ", ColorYellow, prompt, ColorReset)
	}
	
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	
	if input == "" && defaultValue != "" {
		return defaultValue
	}
	return input
}

func askYesNo(prompt string, defaultYes bool) bool {
	reader := bufio.NewReader(os.Stdin)
	defaultStr := "N"
	if defaultYes {
		defaultStr = "Y"
	}
	
	fmt.Printf("%s%s%s [%s%s%s]: ", ColorYellow, prompt, ColorReset, ColorGray, defaultStr, ColorReset)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	
	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}

func (c *Client) Start() error {
	printBanner()
	
	fmt.Printf("%s[INIT]%s Starting with %d peer(s)...\n\n", ColorBlue, ColorReset, len(c.peers))

	for i, peer := range c.peers {
		cp := &ClientPeer{peer: peer}
		c.peerClients[i] = cp
		c.wg.Add(1)
		
		go func(cp *ClientPeer) {
			defer c.wg.Done()
			c.maintainPeerConnection(cp)
		}(cp)
	}

	c.wg.Wait()
	return nil
}

func (c *Client) maintainPeerConnection(cp *ClientPeer) {
	for {
		err := c.connectAndServe(cp)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "Unauthorized") {
				fmt.Printf("%s[ERROR]%s %s\n", ColorRed, ColorReset, errStr)
				os.Exit(1)
			}
			if strings.Contains(errStr, "busy") || strings.Contains(errStr, "BUSY") {
				fmt.Printf("%s[ERROR]%s Subdomain '%s' is already taken by another user\n", 
					ColorRed, ColorReset, c.subdomain)
				os.Exit(1)
			}
		}
		
		time.Sleep(c.reconnectDelay)
	}
}

func (c *Client) connectAndServe(cp *ClientPeer) error {
	conn, err := net.Dial("tcp", cp.peer.Address)
	if err != nil {
		return err
	}
	
	cp.mu.Lock()
	cp.controlConn = conn
	cp.connected = true
	cp.mu.Unlock()

	defer func() {
		cp.mu.Lock()
		cp.connected = false
		cp.mu.Unlock()
		conn.Close()
	}()

	if err := c.register(conn); err != nil {
		return err
	}

	serverHost, _, _ := net.SplitHostPort(cp.peer.Address)
	fmt.Printf("%s[CONNECTED]%s Server: %s (%s) | Site: %s.%s | Local: %s\n", 
		ColorGreen, ColorReset, cp.peer.Name, serverHost, c.subdomain, "ee.com.jm", c.localAddr)
	
	if c.sslMode {
		fmt.Printf("%s[SSL]%s Your site will be available at: https://%s.ee.com.jm\n", 
			ColorMagenta, ColorReset, c.subdomain)
	}

	return c.commandLoop(conn, cp)
}

func (c *Client) register(conn net.Conn) error {
	msg := fmt.Sprintf("REGISTER %s %s\n", c.subdomain, c.authKey)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	
	if _, err := conn.Write([]byte(msg)); err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Time{})

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	conn.SetReadDeadline(time.Time{})
	
	if err != nil {
		return err
	}

	response = strings.TrimSpace(response)

	if strings.HasPrefix(response, "ERROR") {
		parts := strings.SplitN(response, ":", 2)
		reason := "Unknown error"
		if len(parts) > 1 {
			reason = strings.TrimSpace(parts[1])
		}
		
		switch {
		case strings.Contains(response, "Unauthorized"):
			return fmt.Errorf("Unauthorized: wrong auth key for subdomain '%s'", c.subdomain)
		case strings.Contains(response, "Invalid"):
			return fmt.Errorf("Invalid subdomain format")
		default:
			return fmt.Errorf("Registration failed: %s", reason)
		}
	}
	
	if response != "OK" {
		return fmt.Errorf("Unexpected response: %s", response)
	}

	return nil
}

func (c *Client) commandLoop(conn net.Conn, cp *ClientPeer) error {
	reader := bufio.NewReader(conn)
	
	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}

		conn.SetReadDeadline(time.Time{})
		line = strings.TrimSpace(line)
		
		if err := c.handleCommand(line, cp); err != nil {
		}
	}
}

func (c *Client) handleCommand(line string, cp *ClientPeer) error {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return nil
	}

	switch parts[0] {
	case "TUNNEL":
		if len(parts) < 3 {
			return nil
		}

		port, err := net.LookupPort("tcp", parts[1])
		if err != nil {
			return err
		}

		targetSubdomain := parts[2]
		if targetSubdomain != c.subdomain {
			return fmt.Errorf("wrong subdomain")
		}

		go c.handleTunnel(port, cp.peer)

	case "PING":
		cp.mu.Lock()
		if cp.controlConn != nil {
			cp.controlConn.Write([]byte("PONG\n"))
		}
		cp.mu.Unlock()
	}

	return nil
}

func (c *Client) handleTunnel(serverPort int, peer Peer) {
	serverHost, _, _ := net.SplitHostPort(peer.Address)
	
	tunnelAddr := fmt.Sprintf("%s:%d", serverHost, serverPort)
	tunnelConn, err := net.Dial("tcp", tunnelAddr)
	if err != nil {
		return
	}
	defer tunnelConn.Close()

	identMsg := fmt.Sprintf("SUBDOMAIN %s\n", c.subdomain)
	if _, err := tunnelConn.Write([]byte(identMsg)); err != nil {
		return
	}

	localConn, err := net.Dial("tcp", c.localAddr)
	if err != nil {
		return
	}
	defer localConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(localConn, tunnelConn)
		localConn.Close()
		tunnelConn.Close()
	}()

	go func() {
		defer wg.Done()
		io.Copy(tunnelConn, localConn)
		tunnelConn.Close()
		localConn.Close()
	}()

	wg.Wait()
}

func main() {
	printBanner()

	fmt.Printf("%s[CONFIG]%s Please configure your tunnel:\n\n", ColorBlue, ColorReset)
	
	subdomain := askQuestion("Subdomain (e.g., test)", "")
	if subdomain == "" {
		fmt.Printf("%s[ERROR]%s Subdomain is required!\n", ColorRed, ColorReset)
		os.Exit(1)
	}
	
	localPort := askQuestion("Local port to forward", "8000")
	if localPort == "" {
		localPort = "8000"
	}
	
	authKey := askQuestion("Auth Key (secret)", "")
	if authKey == "" {
		fmt.Printf("%s[ERROR]%s Auth key is required!\n", ColorRed, ColorReset)
		os.Exit(1)
	}
	
	sslMode := askYesNo("SSL Mode (HTTPS)", false)
	
	fmt.Println()
	fmt.Printf("%s[INFO]%s Configuration:\n", ColorCyan, ColorReset)
	fmt.Printf("  Subdomain: %s%s%s.%s%s%s\n", ColorGreen, subdomain, ColorReset, ColorYellow, "ee.com.jm", ColorReset)
	fmt.Printf("  Local Port: %s%s%s\n", ColorGreen, localPort, ColorReset)
	fmt.Printf("  Auth Key: %s%s...%s\n", ColorGreen, authKey[:min(8, len(authKey))], ColorReset)
	fmt.Printf("  SSL Mode: %s%s%s\n", ColorGreen, map[bool]string{true: "Yes", false: "No"}[sslMode], ColorReset)
	fmt.Printf("  Peers: %s%d%s\n", ColorGreen, len(PEERS), ColorReset)
	fmt.Println()
	
	localAddr := fmt.Sprintf("127.0.0.1:%s", localPort)
	client := NewClient(PEERS, localAddr, subdomain, authKey, sslMode)

	if err := client.Start(); err != nil {
		log.Fatalf("%s[FATAL]%s %v", ColorRed, ColorReset, err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
