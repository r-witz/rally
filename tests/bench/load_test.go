package bench

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if name := os.Getenv("BENCH_SERVE"); name != "" {
		serveForever(name, os.Getenv("BENCH_ADDR"))
		return
	}
	os.Exit(m.Run())
}

func serveForever(name, addr string) {
	for _, s := range servers {
		if s.name == name {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				panic(err)
			}
			panic((&http.Server{Handler: s.h}).Serve(ln))
		}
	}
	panic("unknown server " + name)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestLoad(t *testing.T) {
	if os.Getenv("BENCH_LOAD") == "" {
		t.Skip("set BENCH_LOAD=1 to run the end-to-end load test")
	}
	serverCPUs := env("BENCH_SERVER_CPUS", "0-3")
	clientCPUs := env("BENCH_CLIENT_CPUS", "4-11")
	conns, _ := strconv.Atoi(env("BENCH_CONNS", "64"))
	duration, _ := time.ParseDuration(env("BENCH_DURATION", "5s"))
	if os.Getenv("BENCH_PINNED") == "" {
		cmd := exec.Command("taskset", "-c", clientCPUs, os.Args[0], "-test.run=^TestLoad$", "-test.v")
		cmd.Env = append(os.Environ(), "BENCH_PINNED=1")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	fmt.Printf("server CPUs %s, client CPUs %s, %d connections, %s per run\n\n", serverCPUs, clientCPUs, conns, duration)
	fmt.Printf("%-11s %-20s %10s %9s %9s %13s\n", "scenario", "server", "req/s", "p50", "p99", "CPU µs/req")
	for _, s := range scenarios {
		for _, srv := range servers {
			r := loadOne(t, srv.name, s, serverCPUs, conns, duration)
			fmt.Printf("%-11s %-20s %10.0f %9s %9s %13.1f\n", s.name, srv.name, r.rps, r.p50, r.p99, r.cpuPerReq)
		}
		fmt.Println()
	}
}

type loadResult struct {
	rps       float64
	p50, p99  time.Duration
	cpuPerReq float64
}

func loadOne(t *testing.T, name string, s scenario, cpus string, conns int, duration time.Duration) loadResult {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cmd := exec.Command("taskset", "-c", cpus, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "BENCH_SERVE="+name, "BENCH_ADDR="+addr, "BENCH_PINNED=")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for i := 0; ; i++ {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		if i > 200 {
			t.Fatalf("server %s did not start: %v", name, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: %s\r\n", s.method, s.target, addr)
	if s.body != "" {
		fmt.Fprintf(&req, "Content-Type: application/json\r\nContent-Length: %d\r\n", len(s.body))
	}
	req.WriteString("\r\n" + s.body)

	run := func(d time.Duration) (int, []time.Duration) {
		var mu sync.Mutex
		var all []time.Duration
		total := 0
		var wg sync.WaitGroup
		deadline := time.Now().Add(d)
		for range conns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, err := net.Dial("tcp", addr)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.Close()
				br := bufio.NewReaderSize(c, 16<<10)
				var lat []time.Duration
				for time.Now().Before(deadline) {
					start := time.Now()
					if _, err := c.Write(req.Bytes()); err != nil {
						t.Error(err)
						return
					}
					if err := readResponse(br, s.status); err != nil {
						t.Error(err)
						return
					}
					lat = append(lat, time.Since(start))
				}
				mu.Lock()
				all = append(all, lat...)
				total += len(lat)
				mu.Unlock()
			}()
		}
		wg.Wait()
		return total, all
	}

	run(duration / 5)
	cpu0 := processCPU(t, cmd.Process.Pid)
	start := time.Now()
	n, lat := run(duration)
	elapsed := time.Since(start)
	cpu := processCPU(t, cmd.Process.Pid) - cpu0
	slices.Sort(lat)
	return loadResult{
		rps:       float64(n) / elapsed.Seconds(),
		p50:       lat[len(lat)/2].Round(time.Microsecond),
		p99:       lat[len(lat)*99/100].Round(time.Microsecond),
		cpuPerReq: float64(cpu.Microseconds()) / float64(n),
	}
}

func readResponse(br *bufio.Reader, want int) error {
	line, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "HTTP/1.1 "+strconv.Itoa(want)) {
		return fmt.Errorf("unexpected status line %q", line)
	}
	length := -1
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if h == "\r\n" {
			break
		}
		if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(k, "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if length < 0 {
		return fmt.Errorf("response without Content-Length")
	}
	_, err = io.CopyN(io.Discard, br, int64(length))
	return err
}

func processCPU(t *testing.T, pid int) time.Duration {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+2:]))
	utime, _ := strconv.ParseInt(fields[11], 10, 64)
	stime, _ := strconv.ParseInt(fields[12], 10, 64)
	return time.Duration(utime+stime) * time.Second / 100
}
