package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/armon/go-socks5"
	"go.uber.org/zap"
)

var ipList []net.IP
var localRand *rand.Rand
var sugar *zap.SugaredLogger

func ipToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}

func randomIP() net.IP {
	if len(ipList) == 0 {
		sugar.Errorw("randomIP called with empty ipList")
		return net.IPv4zero
	}
	return ipList[localRand.Intn(len(ipList))]
}
}

func customDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	localIP := randomIP()
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
				// Error from c.Control itself
				sugar.Errorw("Dialer Control error", "network", network, "address", address, "error", err)
				return fmt.Errorf("rawconn control error: %w", err)
			}
			if opErr != nil {
				// Error from syscall.SetsockoptInt
				sugar.Errorw("SetsockoptInt IP_FREEBIND failed", "fd", uintptr(0), "error", opErr) // fd is not accessible here, log as 0 or remove
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

func validateIPRange(startStr, endStr string) ([]net.IP, error) {
	startIP := net.ParseIP(startStr).To4()
	endIP := net.ParseIP(endStr).To4()
	if startIP == nil || endIP == nil {
		err := fmt.Errorf("invalid IPv4 addresses: start=%s, end=%s", startStr, endStr)
		// No sugar.Errorw here, as this error is returned and handled by the caller
		return nil, err
	}

	startVal := ipToUint32(startIP)
	endVal := ipToUint32(endIP)
	if startVal > endVal {
		err := fmt.Errorf("start IP (%s) must be <= end IP (%s)", startStr, endStr)
		return nil, err
	}

	var ips []net.IP
	// Pre-allocate slice capacity if the range isn't excessively large
	// This is a minor optimization, be cautious with huge ranges.
	// If endVal - startVal + 1 overflows or is too big, this could be an issue.
	// For typical CCDC ranges, it should be fine.
	estimatedSize := endVal - startVal + 1
	if estimatedSize > 0 && estimatedSize < 10000000 { // Cap preallocation
		ips = make([]net.IP, 0, estimatedSize)
	}

	for i := startVal; i <= endVal; i++ {
		// Check for potential overflow if startVal is very small and endVal is very large
		// such that i could wrap around. For IPv4 uint32, this check is relevant if i could become < startVal.
		// However, the loop condition i <= endVal should prevent issues unless endVal is near max uint32.
		ips = append(ips, uint32ToIP(i))
		if i == 0xffffffff && i < endVal { // Max uint32, but loop wants to continue
			break // Avoid overflow in i++
		}
	}
	if len(ips) == 0 {
		// This case should be covered by startIP <= endIP,
		// but as a safeguard if logic changes.
		return nil, fmt.Errorf("no IPs generated for range %s - %s", startStr, endStr)
	}
	return ips, nil
}


func loadIPsFromFile(filePath string) ([]net.IP, error) {
	file, err := os.Open(filePath)
	if err != nil {
		// Wrap error for context
		return nil, fmt.Errorf("failed to open IP file '%s': %w", filePath, err)
	}
	defer file.Close()

	var ips []net.IP
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") { // Skip empty lines and comments
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
		// Wrap error for context
		return nil, fmt.Errorf("error scanning IP file '%s': %w", filePath, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no valid IPs found in file '%s'", filePath)
	}
	return ips, nil
}


func main() {
	// Initialize Zap logger
	// Using NewDevelopment for more verbose output during development.
	// Replace with zap.NewProductionConfig().Build() for production.
	logger, err := zap.NewDevelopment() // Or zap.NewProduction()
	if err != nil {
		// Fallback to standard log if zap fails to initialize
		// log.Fatalf("Failed to initialize zap logger: %v", err)
		fmt.Fprintf(os.Stderr, "Failed to initialize zap logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync() // Flushes buffer, if any
	sugar = logger.Sugar()

	startFlag := flag.String("start", "", "Start IP of the range (e.g., 10.1.0.0)")
	endFlag := flag.String("end", "", "End IP of the range (e.g., 10.100.255.255)")
	fileFlag := flag.String("file", "", "File containing a list of IP addresses (one per line)")
	portFlag := flag.Int("port", 1080, "Port on which the SOCKS5 proxy will listen")
	flag.Parse()

	// var err error // Already declared above for logger

	switch {
	case *fileFlag != "":
		ipList, err = loadIPsFromFile(*fileFlag)
		if err != nil {
			sugar.Fatalf("Failed loading IPs from file: %v", err) // Zap will handle err type
		}
		sugar.Infof("Loaded %d IPs from file: %s", len(ipList), *fileFlag)
	case *startFlag != "" && *endFlag != "":
		ipList, err = validateIPRange(*startFlag, *endFlag)
		if err != nil {
			sugar.Fatalf("Invalid IP range: %v", err) // Zap will handle err type
		}
		sugar.Infof("Using IP range with %d IPs: %s - %s", len(ipList), *startFlag, *endFlag)
	default:
		// log.Fatalf("Usage: -start and -end for IP range OR -file for list of IPs")
		flag.Usage() // Print usage from flags
		sugar.Fatalw("Invalid arguments: Missing IP source (range or file)",
			"usage", "Provide -start and -end flags for an IP range, or -file flag for a list of IPs.",
		)
		os.Exit(1) // Ensure exit after fatal log if flag.Usage() doesn't exit
	}

	if len(ipList) == 0 {
		sugar.Fatal("IP list is empty after processing flags. Cannot start proxy.")
	}

	source := rand.NewSource(time.Now().UnixNano())
	localRand = rand.New(source)

	conf := &socks5.Config{
		Dial:   customDialer,
		Logger: zap.NewStdLog(logger),
	}

	stdZapLog := zap.NewStdLog(logger) // Create a standard logger from zap
	conf.Logger = stdZapLog            // Assign it to the SOCKS5 config

	server, err := socks5.New(conf)
	if err != nil {
		sugar.Fatalf("Error creating SOCKS5 server: %v", err)
	}

	listenAddr := fmt.Sprintf("0.0.0.0:%d", *portFlag)
	sugar.Infof("Starting SOCKS5 server on %s", listenAddr)
	if err := server.ListenAndServe("tcp", listenAddr); err != nil {
		sugar.Fatalf("Error starting SOCKS5 server: %v", err)
	}
}