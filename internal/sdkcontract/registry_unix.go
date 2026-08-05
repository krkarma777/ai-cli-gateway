//go:build !windows

package sdkcontract

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const registryByteLimit = 512

type registryRecord struct {
	kind string
	pid  int
	pgid int
}

type unixFixtureRegistry struct {
	mu        sync.Mutex
	file      *os.File
	protocol  time.Duration
	ready     chan struct{}
	arm       chan struct{}
	stop      chan struct{}
	done      chan struct{}
	armOnce   sync.Once
	readyOnce sync.Once
	stopOnce  sync.Once
	result    cleanupResult
	armed     bool
	complete  bool
	failed    bool
	buffer    []byte
	records   []registryRecord
}

type unixRecoveryRegistry struct {
	path     string
	file     *os.File
	ready    chan struct{}
	stopOnce sync.Once
	result   cleanupResult
}

func startPlatformRegistry(path string, protocol time.Duration) (fixtureRegistry, error) {
	if protocol <= 0 {
		return nil, newError(categoryInvalid)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		return nil, newCleanupError(true)
	}
	rollback := func(file *os.File) (fixtureRegistry, error) {
		return rollbackRegistryConstructor(path, file, os.Remove)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return rollback(nil)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm() != 0o600 {
		return rollback(nil)
	}
	// #nosec G304 -- path is the exact FIFO just created by this function.
	file, err := os.OpenFile(path, os.O_RDWR|syscallNonblock(), 0)
	if err != nil {
		return rollback(nil)
	}
	if err := file.Chmod(0o600); err != nil {
		return rollback(file)
	}
	handleInfo, handleErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if handleErr != nil || pathErr != nil || handleInfo.Mode()&os.ModeNamedPipe == 0 ||
		handleInfo.Mode().Perm() != 0o600 || pathInfo.Mode()&os.ModeSymlink != 0 ||
		pathInfo.Mode()&os.ModeNamedPipe == 0 || pathInfo.Mode().Perm() != 0o600 ||
		!os.SameFile(handleInfo, pathInfo) {
		return rollback(file)
	}
	registry := &unixFixtureRegistry{
		file: file, protocol: protocol, ready: make(chan struct{}), arm: make(chan struct{}, 1),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go registry.drain()
	return registry, nil
}

func rollbackRegistryConstructor(path string, file *os.File, remove func(string) error) (fixtureRegistry, error) {
	if remove == nil {
		return &unixRecoveryRegistry{path: path, file: file, ready: make(chan struct{})}, newCleanupError(false)
	}
	var closeErr error
	if file != nil {
		closeErr = file.Close()
	}
	removeErr := remove(path)
	if closeErr == nil && removeErr == nil {
		return nil, newCleanupError(true)
	}
	return &unixRecoveryRegistry{path: path, file: file, ready: make(chan struct{})}, newCleanupError(false)
}

func (r *unixRecoveryRegistry) Ready() <-chan struct{} {
	if r == nil || r.ready == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return r.ready
}

func (r *unixRecoveryRegistry) StopAndVerify(grace time.Duration) cleanupResult {
	if r == nil || grace <= 0 {
		return cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
	}
	r.stopOnce.Do(func() {
		if r.file != nil {
			_ = r.file.Close()
		}
		r.result = cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
	})
	return r.result
}

func syscallNonblock() int { return unix.O_NONBLOCK }

func (r *unixFixtureRegistry) Ready() <-chan struct{} {
	r.armOnce.Do(func() {
		r.mu.Lock()
		r.armed = true
		r.mu.Unlock()
		select {
		case r.arm <- struct{}{}:
		default:
		}
	})
	return r.ready
}

func (r *unixFixtureRegistry) drain() {
	defer close(r.done)
	interval := r.protocol / 32
	if interval <= 0 {
		interval = r.protocol
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var deadline *time.Timer
	var deadlineChannel <-chan time.Time
	startDeadline := func() {
		if deadline == nil {
			deadline = time.NewTimer(r.protocol)
			deadlineChannel = deadline.C
		}
	}
	defer func() {
		if deadline != nil {
			deadline.Stop()
		}
	}()
	data := make([]byte, 128)
	drainAvailable := func() {
		for {
			n, err := unix.Read(int(r.file.Fd()), data)
			if n > 0 {
				r.mu.Lock()
				if !r.armed {
					r.armed = true
					startDeadline()
				}
				r.consume(data[:n])
				r.mu.Unlock()
			}
			if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				r.mu.Lock()
				r.failed = true
				r.mu.Unlock()
			}
			if n == 0 || err != nil {
				return
			}
		}
	}
	for {
		select {
		case <-r.stop:
			drainAvailable()
			return
		case <-r.arm:
			startDeadline()
		case <-deadlineChannel:
			r.mu.Lock()
			if !r.complete {
				r.failed = true
			}
			r.mu.Unlock()
			deadlineChannel = nil
		case <-ticker.C:
			drainAvailable()
		}
	}
}

func (r *unixFixtureRegistry) consume(data []byte) {
	if r.failed {
		return
	}
	if len(r.buffer)+len(data) > registryByteLimit {
		r.failed = true
		return
	}
	r.buffer = append(r.buffer, data...)
	for {
		index := -1
		for i, value := range r.buffer {
			if value == '\n' {
				index = i
				break
			}
		}
		if index < 0 {
			return
		}
		line := string(r.buffer[:index])
		r.buffer = append(r.buffer[:0], r.buffer[index+1:]...)
		record, ok := parseRegistryRecord(line)
		if !ok || !r.acceptRecord(record) {
			r.failed = true
			return
		}
		if len(r.records) == 2 {
			if len(r.buffer) != 0 {
				r.failed = true
				return
			}
			r.complete = true
			r.readyOnce.Do(func() { close(r.ready) })
			return
		}
	}
}

func parseRegistryRecord(line string) (registryRecord, bool) {
	fields := strings.Split(line, " ")
	if len(fields) != 3 || fields[0] != "provider" && fields[0] != "descendant" {
		return registryRecord{}, false
	}
	if !exactPositiveDecimal(fields[1]) || !exactPositiveDecimal(fields[2]) {
		return registryRecord{}, false
	}
	pid64, errPID := strconv.ParseInt(fields[1], 10, 32)
	pgid64, errPGID := strconv.ParseInt(fields[2], 10, 32)
	if errPID != nil || errPGID != nil || pid64 <= 1 || pgid64 <= 1 {
		return registryRecord{}, false
	}
	return registryRecord{kind: fields[0], pid: int(pid64), pgid: int(pgid64)}, true
}

func exactPositiveDecimal(value string) bool {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func (r *unixFixtureRegistry) acceptRecord(record registryRecord) bool {
	if len(r.records) == 0 {
		if record.kind != "provider" || record.pid != record.pgid {
			return false
		}
		r.records = append(r.records, record)
		return true
	}
	if len(r.records) != 1 || record.kind != "descendant" || record.pid == r.records[0].pid || record.pgid != r.records[0].pgid {
		return false
	}
	r.records = append(r.records, record)
	return true
}

func (r *unixFixtureRegistry) StopAndVerify(grace time.Duration) cleanupResult {
	if r == nil || grace <= 0 {
		return cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
	}
	r.stopOnce.Do(func() { r.result = r.stopAndVerify(grace) })
	return r.result
}

func (r *unixFixtureRegistry) stopAndVerify(grace time.Duration) cleanupResult {
	close(r.stop)
	timer := time.NewTimer(grace)
	select {
	case <-r.done:
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
		_ = r.file.Close()
		return cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
	}
	closeErr := r.file.Close()
	r.mu.Lock()
	armed, complete, failed := r.armed, r.complete, r.failed
	buffered := len(r.buffer)
	records := append([]registryRecord(nil), r.records...)
	r.mu.Unlock()
	safe := cleanupRegistryProcesses(records, grace)
	if armed && len(records) == 0 {
		safe = false
	}
	protocolErr := failed || armed && (!complete || buffered != 0)
	if closeErr != nil || protocolErr {
		return cleanupResult{SafeToRemove: safe, Err: newError(categoryCleanup)}
	}
	if !safe {
		return cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
	}
	return cleanupResult{SafeToRemove: true}
}

func cleanupRegistryProcesses(records []registryRecord, grace time.Duration) bool {
	if len(records) == 0 {
		return true
	}
	pgid := records[0].pgid
	_ = unix.Kill(-pgid, unix.SIGTERM)
	if waitRegistryAbsence(records, grace/2) {
		return true
	}
	_ = unix.Kill(-pgid, unix.SIGKILL)
	return waitRegistryAbsence(records, grace-grace/2)
}

func waitRegistryAbsence(records []registryRecord, grace time.Duration) bool {
	if grace <= 0 {
		return registryAbsent(records)
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	interval := grace / 32
	if interval <= 0 {
		interval = grace
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if registryAbsent(records) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func registryAbsent(records []registryRecord) bool {
	if len(records) == 0 {
		return true
	}
	if !errors.Is(unix.Kill(-records[0].pgid, 0), unix.ESRCH) {
		return false
	}
	for _, record := range records {
		if !errors.Is(unix.Kill(record.pid, 0), unix.ESRCH) {
			return false
		}
	}
	return true
}
