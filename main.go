package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/armon/go-socks5"
)

// Global variables holding the numeric representation of the IP range.
var ipRangeStart, ipRangeEnd uint32

// ipToUint32 converts a net.IP (assumed to be IPv4) to a uint32 representation.
func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return binary.BigEndian.Uint32(ip)
}

// uint32ToIP converts a uint32 to a net.IP (IPv4).
func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}

// randomIP computes a random IP within the global ipRangeStart to ipRangeEnd values.
func randomIP() net.IP {
	// Calculate total number of addresses in the range.
	total := ipRangeEnd - ipRangeStart + 1
	// Pick a random offset.
	offset := uint32(rand.Intn(int(total)))
	return uint32ToIP(ipRangeStart + offset)
}

// customDialer creates an outbound connection bound to a random IP from the chosen range.
// It also uses the IP_FREEBIND socket option, which allows binding to non-local addresses.
func customDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	localIP := randomIP()
	localAddr := &net.TCPAddr{
		IP: localIP,
	}
	dialer := &net.Dialer{
		LocalAddr: localAddr,
		Timeout:   10 * time.Second,
		// Use Control to enable IP_FREEBIND on the socket.
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				// Set the IP_FREEBIND option.
				err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_FREEBIND, 1)
			})
			return err
		},
	}
	return dialer.DialContext(ctx, network, addr)
}

// validateIPRange checks that the provided IP strings are valid IPv4 addresses and that start <= end.
func validateIPRange(startStr, endStr string) (net.IP, net.IP, error) {
	startIP := net.ParseIP(startStr)
	if startIP == nil || startIP.To4() == nil {
		return nil, nil, fmt.Errorf("start IP (%s) is not a valid IPv4 address", startStr)
	}
	endIP := net.ParseIP(endStr)
	if endIP == nil || endIP.To4() == nil {
		return nil, nil, fmt.Errorf("end IP (%s) is not a valid IPv4 address", endStr)
	}

	// Convert to numeric form for comparison.
	startVal := ipToUint32(startIP)
	endVal := ipToUint32(endIP)
	if startVal > endVal {
		return nil, nil, fmt.Errorf("start IP (%s) must be less than or equal to end IP (%s)", startStr, endStr)
	}
	return startIP.To4(), endIP.To4(), nil
}

func main() {
	// Define command-line flags for start and end IP.
	startFlag := flag.String("start", "", "Start IP of the range (e.g., 10.1.0.0)")
	endFlag := flag.String("end", "", "End IP of the range (e.g., 10.100.255.255)")
	portFlag := flag.Int("port", 1080, "Port on which the SOCKS5 proxy will listen")
	flag.Parse()

	// Check that the start and end flags were provided.
	if *startFlag == "" || *endFlag == "" {
		fmt.Println("Usage: scoreproxy -start <start-IP> -end <end-IP> [-port <port>]")
		os.Exit(1)
	}

	// Validate the IP range.
	startIP, endIP, err := validateIPRange(*startFlag, *endFlag)
	if err != nil {
		log.Fatalf("Invalid IP range: %v", err)
	}

	// Compute the numeric representations for use in randomIP().
	ipRangeStart = ipToUint32(startIP)
	ipRangeEnd = ipToUint32(endIP)
	log.Printf("Using IP range from %s (%d) to %s (%d)",
		startIP, ipRangeStart, endIP, ipRangeEnd)

	// Seed the random number generator.
	rand.Seed(time.Now().UnixNano())

	// Configure the SOCKS5 server to use our custom dialer.
	conf := &socks5.Config{
		Dial: customDialer,
	}

	server, err := socks5.New(conf)
	if err != nil {
		log.Fatalf("Error creating SOCKS5 server: %v", err)
	}

	// Prepare the listen address using the provided port.
	listenAddr := fmt.Sprintf("0.0.0.0:%d", *portFlag)
	log.Printf("Starting SOCKS5 server on %s", listenAddr)
	if err := server.ListenAndServe("tcp", listenAddr); err != nil {
		log.Fatalf("Error starting SOCKS5 server: %v", err)
	}
}

