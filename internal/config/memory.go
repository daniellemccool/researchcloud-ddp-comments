package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Memory limits of one collector pair and the box memory kept for everything
// else (ADR-0023). Measured 2026-11-05 on a 16 GB sandbox: Firefox peak
// 2328.6 MiB over three TikTok comment sessions; proxy peak 20.8 MiB.
const (
	FirefoxMemory int64 = 3 << 30
	ProxyMemory   int64 = 256 << 20
	MemoryReserve int64 = 4 << 30
)

// ReadMemTotal parses the "MemTotal:" kB line of a meminfo file and returns
// bytes.
func ReadMemTotal(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 1 {
			return 0, fmt.Errorf("%s: malformed MemTotal line", path)
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kb < 0 {
			return 0, fmt.Errorf("%s: malformed MemTotal line", path)
		}
		return kb * 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%s: no MemTotal line", path)
}

// CapByMemory returns min(maxRunning, max(1, (memTotal-MemoryReserve) /
// (FirefoxMemory+ProxyMemory))) and whether the memory term was the smaller.
// MemorySeats returns how many pairs memTotal allows: (memTotal-MemoryReserve)
// / (FirefoxMemory+ProxyMemory), at least 1.
func MemorySeats(memTotal int64) int {
	return int(max((memTotal-MemoryReserve)/(FirefoxMemory+ProxyMemory), 1))
}

// CapByMemory returns min(maxRunning, MemorySeats(memTotal)) and whether the
// memory term was the smaller.
func CapByMemory(maxRunning int, memTotal int64) (n int, capped bool) {
	if byMem := MemorySeats(memTotal); byMem < maxRunning {
		return byMem, true
	}
	return maxRunning, false
}
