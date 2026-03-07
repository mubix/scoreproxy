package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/things-go/go-socks5"
	"go.uber.org/zap"
)

// ipSource abstracts IP selection so range mode doesn't materialize millions of IPs.
type ipSource struct {
	mu sync.Mutex
	rng *rand.Rand

	// file mode: explicit list of IPs
	ipList []net.IP

	// range mode: just store bounds
	useRange   bool
	rangeStart uint32
	rangeEnd   uint32
}

func newIPSource() *ipSource {
	return &ipSource{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *ipSource) setRange(start, end uint32) {
	s.useRange = true
	s.rangeStart = start
	s.rangeEnd = end
}

func (s *ipSource) setList(ips []net.IP) {
	s.useRange = false
	s.ipList = ips
}

func (s *ipSource) count() uint32 {
	if s.useRange {
		return s.rangeEnd - s.rangeStart + 1
	}
	return uint32(len(s.ipList))
}

func (s *ipSource) randomIP() net.IP {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.useRange {
		n := s.rangeEnd - s.rangeStart + 1
		return uint32ToIP(s.rangeStart + uint32(s.rng.Int63n(int64(n))))
	}

	if len(s.ipList) == 0 {
		return net.IPv4zero
	}
	return s.ipList[s.rng.Intn(len(s.ipList))]
}

var source *ipSource
var sugar *zap.SugaredLogger

func ipToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}

func customDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	localIP := source.randomIP()
	if localIP == nil || localIP.IsUnspecified() {
		err := fmt.Errorf("failed to get a valid random IP for dialing")
		sugar.Errorw("CustomDialer: No valid local IP", "error", err)
		return nil, err
	}
	localAddr := &net.TCPAddr{
		IP: localIP,
	}

	sugar.Debugw("Dialing with custom local IP",
		"network", network,
		"remote_addr", addr,
		"local_ip", localIP.String(),
	)

	dialer := &net.Dialer{
		LocalAddr: localAddr,
		Timeout:   10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			err := c.Control(func(fd uintptr) {
				opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_FREEBIND, 1)
			})
			if err != nil {
				sugar.Errorw("Dialer Control error", "network", network, "address", address, "error", err)
				return fmt.Errorf("rawconn control error: %w", err)
			}
			if opErr != nil {
				sugar.Errorw("SetsockoptInt IP_FREEBIND failed", "error", opErr)
				return fmt.Errorf("setsockoptint IP_FREEBIND: %w", opErr)
			}
			return nil
		},
	}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		sugar.Errorw("Custom dial failed",
			"network", network,
			"remote_addr", addr,
			"local_ip", localIP.String(),
			"error", err,
		)
		return nil, fmt.Errorf("custom dialer: %w", err)
	}
	sugar.Infow("Successfully established connection",
		"network", network,
		"remote_addr", addr,
		"local_addr", conn.LocalAddr().String(),
		"remote_conn_addr", conn.RemoteAddr().String(),
	)
	return conn, nil
}

func validateIPRange(startStr, endStr string) (uint32, uint32, error) {
	startIP := net.ParseIP(startStr).To4()
	endIP := net.ParseIP(endStr).To4()
	if startIP == nil || endIP == nil {
		return 0, 0, fmt.Errorf("invalid IPv4 addresses: start=%s, end=%s", startStr, endStr)
	}

	startVal := ipToUint32(startIP)
	endVal := ipToUint32(endIP)
	if startVal > endVal {
		return 0, 0, fmt.Errorf("start IP (%s) must be <= end IP (%s)", startStr, endStr)
	}

	return startVal, endVal, nil
}

func parseCIDR(cidrStr string) (uint32, uint32, error) {
	_, ipNet, err := net.ParseCIDR(cidrStr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid CIDR notation '%s': %w", cidrStr, err)
	}

	if ipNet.IP.To4() == nil {
		return 0, 0, fmt.Errorf("only IPv4 CIDRs are supported, got: %s", cidrStr)
	}

	networkAddr := ipToUint32(ipNet.IP.To4())
	mask := binary.BigEndian.Uint32(ipNet.Mask)

	// First usable host = network + 1, last usable host = broadcast - 1
	broadcastAddr := networkAddr | ^mask
	ones, bits := ipNet.Mask.Size()

	if bits != 32 {
		return 0, 0, fmt.Errorf("expected IPv4 mask, got /%d bits", bits)
	}

	// For /31 and /32 use the full range as-is (point-to-point or single host)
	if ones >= 31 {
		return networkAddr, broadcastAddr, nil
	}

	startHost := networkAddr + 1
	endHost := broadcastAddr - 1

	if startHost > endHost {
		return 0, 0, fmt.Errorf("CIDR %s produces no usable host addresses", cidrStr)
	}

	return startHost, endHost, nil
}

func loadIPsFromFile(filePath string) ([]net.IP, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open IP file '%s': %w", filePath, err)
	}
	defer file.Close()

	var ips []net.IP
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if ip := net.ParseIP(line).To4(); ip != nil {
			ips = append(ips, ip)
		} else {
			sugar.Warnw("Ignoring invalid IP address in file",
				"file", filePath,
				"line_number", lineNumber,
				"ip_string", line,
			)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning IP file '%s': %w", filePath, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no valid IPs found in file '%s'", filePath)
	}
	return ips, nil
}

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize zap logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()
	sugar = logger.Sugar()

	startFlag := flag.String("start", "", "Start IP of the range (e.g., 10.1.0.0)")
	endFlag := flag.String("end", "", "End IP of the range (e.g., 10.100.255.255)")
	cidrFlag := flag.String("cidr", "", "CIDR range to use (e.g., 10.0.0.0/9). Excludes network and broadcast addresses.")
	fileFlag := flag.String("file", "", "File containing a list of IP addresses (one per line)")
	portFlag := flag.Int("port", 1080, "Port on which the SOCKS5 proxy will listen")
	flag.Parse()

	source = newIPSource()

	// Count how many input modes were specified to catch conflicts
	modeCount := 0
	if *fileFlag != "" {
		modeCount++
	}
	if *startFlag != "" || *endFlag != "" {
		modeCount++
	}
	if *cidrFlag != "" {
		modeCount++
	}

	if modeCount == 0 {
		sugar.Error("No IP source specified.")
		fmt.Fprintf(os.Stderr, "\nUsage: specify exactly one of:\n")
		fmt.Fprintf(os.Stderr, "  -start/-end   IP range\n")
		fmt.Fprintf(os.Stderr, "  -cidr         CIDR block\n")
		fmt.Fprintf(os.Stderr, "  -file         file with IP list\n\n")
		flag.Usage()
		os.Exit(1)
	}
	if modeCount > 1 {
		sugar.Fatal("Multiple IP sources specified. Use exactly one of: -start/-end, -cidr, or -file")
	}

	switch {
	case *fileFlag != "":
		ips, err := loadIPsFromFile(*fileFlag)
		if err != nil {
			sugar.Fatalf("Failed loading IPs from file: %v", err)
		}
		source.setList(ips)
		sugar.Infof("Loaded %d IPs from file: %s", len(ips), *fileFlag)

	case *cidrFlag != "":
		start, end, err := parseCIDR(*cidrFlag)
		if err != nil {
			sugar.Fatalf("Invalid CIDR: %v", err)
		}
		source.setRange(start, end)
		sugar.Infof("Using CIDR %s → %s - %s (%d usable hosts)",
			*cidrFlag, uint32ToIP(start), uint32ToIP(end), end-start+1)

	case *startFlag != "" && *endFlag != "":
		start, end, err := validateIPRange(*startFlag, *endFlag)
		if err != nil {
			sugar.Fatalf("Invalid IP range: %v", err)
		}
		source.setRange(start, end)
		sugar.Infof("Using IP range %s - %s (%d IPs)", *startFlag, *endFlag, end-start+1)

	default:
		// Catches case where only -start or only -end was given
		sugar.Fatal("Both -start and -end are required when using range mode")
	}

	if source.count() == 0 {
		sugar.Fatal("IP source is empty after processing flags. Cannot start proxy.")
	}

	server := socks5.NewServer(
		socks5.WithDial(customDialer),
		socks5.WithLogger(socks5.NewLogger(zap.NewStdLog(logger))),
	)

	listenAddr := fmt.Sprintf("0.0.0.0:%d", *portFlag)

	// Graceful shutdown on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start listener manually so we can shut it down via context
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", listenAddr)
	if err != nil {
		sugar.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}

	sugar.Infof("Starting SOCKS5 server on %s (%d source IPs available)", listenAddr, source.count())
	sugar.Info("Note: only TCP connections are rotated. UDP is not currently supported.")

	// Serve in a goroutine so we can wait on the context
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		sugar.Info("Shutdown signal received, closing listener...")
		listener.Close()
		// Wait briefly for serve to return
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			sugar.Warn("Timed out waiting for server to stop")
		}
		sugar.Info("SOCKS5 server stopped.")
	case err := <-errCh:
		if err != nil {
			sugar.Fatalf("SOCKS5 server error: %v", err)
		}
	}
}