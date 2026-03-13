package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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

type TunnelData struct {
	Port    int
	AuthKey string
}

type Server struct {
	id           string
	controlPort  string
	publicPort   string
	domain       string
	store        sync.Map
	clients      map[string]net.Conn
	clientsMu    sync.RWMutex
	tunnelChan   map[string]chan net.Conn
	tunnelMu     sync.RWMutex
	tunnelPort   int
}

func NewServer(id, controlPort, publicPort, domain string) *Server {
	return &Server{
		id:          id,
		controlPort: controlPort,
		publicPort:  publicPort,
		domain:      domain,
		clients:     make(map[string]net.Conn),
		tunnelChan:  make(map[string]chan net.Conn),
	}
}

func logWithTime(color, prefix, message string) {
	timestamp := time.Now().Format("15:04:05")
	log.Printf("%s[%s]%s %s%s%s %s", 
		ColorGray, timestamp, ColorReset,
		color, prefix, ColorReset,
		message)
}

func (s *Server) Start() error {
	controlListener, err := net.Listen("tcp", s.controlPort)
	if err != nil {
		return fmt.Errorf("control listener failed: %w", err)
	}
	defer controlListener.Close()

	tunnelListener, err := net.Listen("tcp", ":0")
	if err != nil {
		return fmt.Errorf("tunnel listener failed: %w", err)
	}
	defer tunnelListener.Close()
	
	s.tunnelPort = tunnelListener.Addr().(*net.TCPAddr).Port

	publicListener, err := net.Listen("tcp", s.publicPort)
	if err != nil {
		return fmt.Errorf("public listener failed: %w", err)
	}
	defer publicListener.Close()

	requestChan := make(chan *UserRequest, 100)

	go s.handlePublicConnections(publicListener, requestChan)
	go s.handleTunnelConnections(tunnelListener)
	go s.processUserRequests(requestChan)

	logWithTime(ColorCyan, "[BONSAI]", fmt.Sprintf("Server %s started", s.id))
	logWithTime(ColorCyan, "[BONSAI]", fmt.Sprintf("Domain: *.%s", s.domain))
	logWithTime(ColorCyan, "[BONSAI]", fmt.Sprintf("Control: %s | Public: %s | Tunnel: %d", 
		s.controlPort, s.publicPort, s.tunnelPort))

	for {
		conn, err := controlListener.Accept()
		if err != nil {
			continue
		}
		go s.handleControlConnection(conn)
	}
}

type UserRequest struct {
	Conn        net.Conn
	InitialData []byte
	Host        string
	Subdomain   string
	IsSSL       bool
}

func (s *Server) handlePublicConnections(listener net.Listener, requestChan chan<- *UserRequest) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go func(userConn net.Conn) {
			initialData, host, isSSL, err := s.sniffHTTPHost(userConn)
			if err != nil {
				s.sendHTTPError(userConn, "Bad Request", 400)
				userConn.Close()
				return
			}

			subdomain := s.extractSubdomain(host)
			if subdomain == "" {
				s.sendHTTPError(userConn, "Invalid subdomain", 400)
				userConn.Close()
				return
			}

			if _, ok := s.store.Load(subdomain); !ok {
				s.sendHTTPError(userConn, "Not found: "+subdomain, 404)
				userConn.Close()
				return
			}

			select {
			case requestChan <- &UserRequest{
				Conn:        userConn,
				InitialData: initialData,
				Host:        host,
				Subdomain:   subdomain,
				IsSSL:       isSSL,
			}:
			default:
				s.sendHTTPError(userConn, "Server busy", 503)
				userConn.Close()
			}
		}(conn)
	}
}

func (s *Server) sniffHTTPHost(conn net.Conn) ([]byte, string, bool, error) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, "", false, err
	}
	data := buf[:n]

	host, isSSL := s.parseHostHeader(data)
	if host == "" {
		return nil, "", false, fmt.Errorf("host not found")
	}

	return data, host, isSSL, nil
}

func (s *Server) parseHostHeader(data []byte) (string, bool) {
	reader := bufio.NewReader(bytes.NewReader(data))
	req, err := http.ReadRequest(reader)
	if err != nil {
		return s.extractHostManual(string(data)), false
	}
	
	isSSL := req.Header.Get("X-Forwarded-Proto") == "https"
	
	return req.Host, isSSL
}

func (s *Server) extractHostManual(data string) string {
	lines := strings.Split(data, "\r\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func (s *Server) extractSubdomain(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	if s.domain != "" {
		domain := strings.ToLower(s.domain)
		if strings.HasSuffix(host, "."+domain) {
			return strings.TrimSuffix(host, "."+domain)
		}
		if host == domain {
			return ""
		}
	}

	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return parts[0]
	}
	return ""
}

func (s *Server) processUserRequests(requestChan <-chan *UserRequest) {
	for req := range requestChan {
		go s.handleUserRequest(req)
	}
}

func (s *Server) handleUserRequest(req *UserRequest) {
	client := s.getClient(req.Subdomain)
	if client == nil {
		s.sendHTTPError(req.Conn, "Client disconnected", 503)
		req.Conn.Close()
		return
	}

	if err := s.signalNewTunnel(client, req.Subdomain); err != nil {
		s.sendHTTPError(req.Conn, "Tunnel error", 503)
		req.Conn.Close()
		return
	}

	tunnelConn, err := s.waitForTunnel(req.Subdomain, 10*time.Second)
	if err != nil {
		s.sendHTTPError(req.Conn, "Tunnel timeout", 504)
		req.Conn.Close()
		return
	}
	defer tunnelConn.Close()

	data := req.InitialData
	if req.IsSSL && !bytes.Contains(data, []byte("X-Forwarded-Proto")) {
		data = s.addForwardedProto(data, "https")
	}

	if len(data) > 0 {
		tunnelConn.Write(data)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(tunnelConn, req.Conn)
		tunnelConn.Close()
	}()

	go func() {
		defer wg.Done()
		io.Copy(req.Conn, tunnelConn)
		req.Conn.Close()
	}()

	wg.Wait()
}

func (s *Server) addForwardedProto(data []byte, proto string) []byte {
	idx := bytes.Index(data, []byte("\r\n\r\n"))
	if idx == -1 {
		idx = bytes.Index(data, []byte("\n\n"))
		if idx == -1 {
			return data
		}
		return append(data[:idx], append([]byte(fmt.Sprintf("\nX-Forwarded-Proto: %s", proto)), data[idx:]...)...)
	}
	return append(data[:idx], append([]byte(fmt.Sprintf("\r\nX-Forwarded-Proto: %s", proto)), data[idx:]...)...)
}

func (s *Server) signalNewTunnel(client net.Conn, subdomain string) error {
	msg := fmt.Sprintf("TUNNEL %d %s\n", s.tunnelPort, subdomain)
	client.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer client.SetWriteDeadline(time.Time{})
	
	_, err := client.Write([]byte(msg))
	return err
}

func (s *Server) waitForTunnel(subdomain string, timeout time.Duration) (net.Conn, error) {
	ch := s.getOrCreateTunnelChan(subdomain)
	
	select {
	case conn := <-ch:
		return conn, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout")
	}
}

func (s *Server) getOrCreateTunnelChan(subdomain string) chan net.Conn {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	
	if ch, ok := s.tunnelChan[subdomain]; ok {
		return ch
	}
	
	ch := make(chan net.Conn, 5)
	s.tunnelChan[subdomain] = ch
	return ch
}

func (s *Server) handleTunnelConnections(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go func(tunnelConn net.Conn) {
			tunnelConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReader(tunnelConn)
			line, err := reader.ReadString('\n')
			tunnelConn.SetReadDeadline(time.Time{})
			
			if err != nil {
				tunnelConn.Close()
				return
			}

			line = strings.TrimSpace(line)
			parts := strings.Fields(line)
			if len(parts) != 2 || parts[0] != "SUBDOMAIN" {
				tunnelConn.Close()
				return
			}

			subdomain := parts[1]
			ch := s.getOrCreateTunnelChan(subdomain)
			
			select {
			case ch <- tunnelConn:
			default:
				tunnelConn.Close()
			}
		}(conn)
	}
}

func (s *Server) handleControlConnection(conn net.Conn) {
	defer conn.Close()

	remoteAddr := conn.RemoteAddr().String()
	remoteIP, _, _ := net.SplitHostPort(remoteAddr)

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	conn.SetReadDeadline(time.Time{})

	line = strings.TrimSpace(line)
	parts := strings.Fields(line)
	if len(parts) != 3 || parts[0] != "REGISTER" {
		conn.Write([]byte("ERROR: Invalid format\n"))
		return
	}

	subdomain := strings.ToLower(parts[1])
	authKey := parts[2]
	
	if !s.isValidSubdomain(subdomain) {
		conn.Write([]byte("ERROR: Invalid subdomain\n"))
		return
	}

	if existingData, loaded := s.store.Load(subdomain); loaded {
		existing := existingData.(TunnelData)
		if existing.AuthKey != authKey {
			logWithTime(ColorRed, "[AUTH-FAIL]", fmt.Sprintf("Subdomain: %s | IP: %s | Reason: wrong key", 
				subdomain, remoteIP))
			conn.Write([]byte("ERROR: Unauthorized\n"))
			return
		}
		logWithTime(ColorYellow, "[RECONNECT]", fmt.Sprintf("Subdomain: %s | IP: %s", subdomain, remoteIP))
	} else {
		logWithTime(ColorGreen, "[REGISTER]", fmt.Sprintf("Subdomain: %s | IP: %s | Key: %s...", 
			subdomain, remoteIP, authKey[:min(8, len(authKey))]))
	}

	s.store.Store(subdomain, TunnelData{
		Port:    s.tunnelPort,
		AuthKey: authKey,
	})

	s.setClient(subdomain, conn)

	conn.Write([]byte("OK\n"))
	logWithTime(ColorCyan, "[ACTIVE]", fmt.Sprintf("%s.%s | Port: %d | IP: %s", 
		subdomain, s.domain, s.tunnelPort, remoteIP))

	s.keepaliveLoop(conn, subdomain)

	s.deleteClient(subdomain)
	s.store.Delete(subdomain)
	
	logWithTime(ColorGray, "[DISCONNECT]", fmt.Sprintf("%s.%s", subdomain, s.domain))
}

func (s *Server) isValidSubdomain(subdomain string) bool {
	if subdomain == "" {
		return false
	}
	for _, c := range subdomain {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func (s *Server) setClient(subdomain string, conn net.Conn) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	s.clients[subdomain] = conn
}

func (s *Server) getClient(subdomain string) net.Conn {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	return s.clients[subdomain]
}

func (s *Server) deleteClient(subdomain string) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	delete(s.clients, subdomain)
}

func (s *Server) keepaliveLoop(conn net.Conn, subdomain string) {
	reader := bufio.NewReader(conn)
	
	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		line, err := reader.ReadString('\n')
		
		if err != nil {
			return
		}

		line = strings.TrimSpace(line)
		if line == "PONG" {
			continue
		}
	}
}

func (s *Server) sendHTTPError(conn net.Conn, message string, code int) {
	statusText := http.StatusText(code)
	if statusText == "" {
		statusText = "Error"
	}
	
	response := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", 
		code, statusText, len(message), message)
	conn.Write([]byte(response))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	var (
		serverID    = flag.String("id", "server-1", "Unique server ID")
		controlPort = flag.String("control", ":7000", "Control port for client connections")
		publicPort  = flag.String("public", ":8080", "Public port for HTTP requests")
		domain      = flag.String("domain", "ee.com.jm", "Base domain for subdomains")
	)
	flag.Parse()

	server := NewServer(*serverID, *controlPort, *publicPort, *domain)
	
	if err := server.Start(); err != nil {
		log.Fatalf("[SERVER] Failed: %v", err)
	}
}
